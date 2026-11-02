// Package winfs is the shared Windows filesystem primitive layer of the haft
// Windows port (ADR-091 option B). It gives the Windows equivalents of the Unix
// guarantees that haft relies on, as mapped in ADR-092:
//
//   - G1, G2: a held directory handle ([Dir]) with single-component,
//     handle-relative, no-follow opens, creates, removes, renames and links;
//   - G2, G6, sameFilesystem: FILE_ID_INFO identity ([FileID]);
//   - G3, G4: exclusive create and install without replace;
//   - G5: LockFileEx file locks and share-mode directory locks ([DirLock]);
//   - G6: private files (explicit owner plus protected owner-only DACL);
//   - G7: directory durability ([SyncDirectory]).
//
// Every implementation file is Windows-only (the _windows file-name suffix is
// its build constraint), so on Linux and macOS the package is empty apart from
// this documentation. Only golang.org/x/sys/windows is used; no cgo.
package winfs
