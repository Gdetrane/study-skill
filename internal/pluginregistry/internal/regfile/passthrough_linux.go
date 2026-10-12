package regfile

import "golang.org/x/sys/unix"

// passThrough opens a folder to pass through it and nothing more: O_PATH.
// The descriptor names the folder, so that a name can be looked up beside
// it, and can neither list the folder nor read it. Linux asks no permission
// on the folder itself for that. Looking a name up beside the descriptor
// then needs the permission to pass through the folder, as it does by a
// path.
const passThrough = unix.O_PATH
