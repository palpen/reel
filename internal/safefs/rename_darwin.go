//go:build darwin && cgo

package safefs

/*
#include <stdio.h>
#include <stdlib.h>
#include <errno.h>
static int reel_rename(int a, const char *src, int b, const char *dst) {
 if (renameatx_np(a, src, b, dst, RENAME_EXCL) == 0) return 0;
 return errno;
}
*/
import "C"
import (
	"syscall"
	"unsafe"
)

func renameExclusive(a int, src string, b int, dst string) error {
	s := C.CString(src)
	defer C.free(unsafe.Pointer(s))
	d := C.CString(dst)
	defer C.free(unsafe.Pointer(d))
	if e := C.reel_rename(C.int(a), s, C.int(b), d); e != 0 {
		return syscall.Errno(e)
	}
	return nil
}
