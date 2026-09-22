package main

// localAPISocket returns the named pipe on which tswipoexp.exe serves
// the LocalAPI. It must match localAPIPipeName in the root package.
func localAPISocket() string { return `\\.\pipe\tswipoexp` }
