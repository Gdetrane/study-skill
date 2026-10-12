package regfile

// passThrough opens a folder to pass through it and nothing more. With
// O_DIRECTORY, which every folder is opened with, it is O_SEARCH: macOS has
// it from version 13, and asks for the permission to search the folder
// where an ordinary open asks for the one to list it. On its own the flag
// is O_EXEC, which golang.org/x/sys/unix does not name, so it is spelled
// out as <sys/fcntl.h> has it.
//
// macOS 12 does not know the flag and opens the folder to list it, as if
// the flag were not given. There a folder that may not be listed stops the
// walk, as it did before.
const passThrough = 0x40000000
