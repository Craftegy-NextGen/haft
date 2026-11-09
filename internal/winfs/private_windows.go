package winfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protection selects the security descriptor that a newly created file or
// directory receives. It has no effect when an open finds an existing entry.
type Protection int

const (
	// Inherited leaves security to the parent directory's inheritable ACEs and
	// the token defaults, the counterpart of a Unix 0644/0755 create.
	Inherited Protection = iota
	// Private sets the owner explicitly to the token user and a protected DACL
	// that allows only that user, SYSTEM and Administrators (ADR-092 O2, 2a):
	// the counterpart of a Unix 0600/0700 create.
	Private
)

// currentUser is the token user SID of this process. The owner of a private
// file is always this SID, never the token default owner, because an elevated
// token's default owner is the Administrators group.
var currentUser = sync.OnceValues(func() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("winfs: read token user: %w", err)
	}
	return user.User.Sid.Copy()
})

// descriptor returns the security descriptor for a create, or nil for
// Inherited.
func (protection Protection) descriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	switch protection {
	case Inherited:
		return nil, nil
	case Private:
		return privateDescriptor(directory)
	}
	return nil, fmt.Errorf("winfs: unknown protection %d: %w", protection, fs.ErrInvalid)
}

func privateDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := currentUser()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	owner := user.String()
	sddl := "O:" + owner + "D:P" +
		allowFullAccess(inheritance, owner) +
		allowFullAccess(inheritance, "SY") +
		allowFullAccess(inheritance, "BA")
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("winfs: build private security descriptor: %w", err)
	}
	return descriptor, nil
}

func allowFullAccess(inheritance string, trustee string) string {
	return "(A;" + inheritance + ";FA;;;" + trustee + ")"
}

// CheckPrivate accepts file only when its owner is the token user SID, its
// DACL is protected and allows nobody but that user, SYSTEM and Administrators
// (deny entries are allowed), and it has exactly one link. A file owned by the
// Administrators group fails with ErrElevatedOwner and a repair hint. It fails
// closed: every failure, including one to read the facts (closed file, token,
// security descriptor, link count), wraps ErrNotPrivate. The handle needs
// READ_CONTROL, which every winfs open requests.
func CheckPrivate(file *os.File) error {
	err := withFileHandle(file, func(handle windows.Handle) error {
		return checkPrivateHandle(handle, nameOf(file))
	})
	if err != nil {
		if !errors.Is(err, ErrNotPrivate) {
			err = fmt.Errorf("%w: %w", ErrNotPrivate, err)
		}
		return &os.PathError{Op: "check private", Path: nameOf(file), Err: err}
	}
	return nil
}

func checkPrivateHandle(handle windows.Handle, path string) error {
	user, err := currentUser()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotPrivate, err)
	}
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("%w: read security descriptor: %w", ErrNotPrivate, err)
	}
	if err := checkOwner(descriptor, user, path); err != nil {
		return err
	}
	if err := checkOwnerOnlyDACL(descriptor, user); err != nil {
		return fmt.Errorf("%w: %w", ErrNotPrivate, err)
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &data); err != nil {
		return fmt.Errorf("%w: query link count: %w", ErrNotPrivate, err)
	}
	if data.NumberOfLinks != 1 {
		return fmt.Errorf("%w: %d links, want 1", ErrNotPrivate, data.NumberOfLinks)
	}
	return nil
}

func checkOwner(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID, path string) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("%w: read owner: %w", ErrNotPrivate, err)
	}
	if owner == nil {
		return fmt.Errorf("%w: no owner", ErrNotPrivate)
	}
	if owner.Equals(user) {
		return nil
	}
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		account := accountName(user)
		return fmt.Errorf(
			"%w: %w: owner is BUILTIN\\Administrators, not the current user %s; "+
				"repair: delete the file so haft recreates it, or from an elevated prompt run "+
				"icacls \"%s\" /setowner \"%s\"",
			ErrNotPrivate,
			ErrElevatedOwner,
			account,
			path,
			account,
		)
	}
	return fmt.Errorf("%w: owner %s is not the current user %s", ErrNotPrivate, owner.String(), user.String())
}

func checkOwnerOnlyDACL(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read security descriptor control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("DACL is not protected from inheritance")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	if dacl == nil {
		return errors.New("NULL DACL grants everyone full access")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read ACE %d: %w", index, err)
		}
		if err := checkACE(ace, user); err != nil {
			return err
		}
	}
	return nil
}

func checkACE(ace *windows.ACCESS_ALLOWED_ACE, user *windows.SID) error {
	switch ace.Header.AceType {
	case windows.ACCESS_DENIED_ACE_TYPE:
		return nil
	case windows.ACCESS_ALLOWED_ACE_TYPE:
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) // #nosec G103 -- the SID starts at SidStart.
		if trustee.Equals(user) ||
			trustee.IsWellKnown(windows.WinLocalSystemSid) ||
			trustee.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return nil
		}
		return fmt.Errorf("DACL allows access to %s", trustee.String())
	}
	return fmt.Errorf("DACL has an ACE of unsupported type %d", ace.Header.AceType)
}

// accountName renders a SID as DOMAIN\user for messages, or as the SID string.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain == "" {
		return account
	}
	return domain + `\` + account
}
