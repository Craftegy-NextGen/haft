package initfs

// volumeRootResourcesAllowed reports whether a filesystem root may be a
// publication coordination resource. On Windows a project may be rooted at a
// volume root such as `D:\`, and its publication resources include that root.
const volumeRootResourcesAllowed = true
