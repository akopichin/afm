//go:build windows

package pipeline

import (
	"errors"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openComponent opens the single path element name inside dir. FILE_OPEN_REPARSE_POINT opens a symlink or junction
// itself rather than its target, and any reparse point is then refused.
func openComponent(dir *os.File, name string) (*os.File, error) {
	// A separator or stream colon would let one NT name cross path elements without this check.
	if strings.ContainsAny(name, `\/:`) {
		return nil, errors.New("invalid path element")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(dir.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var h windows.Handle
	err = windows.NtCreateFile(&h, windows.FILE_GENERIC_READ, &attrs, &windows.IO_STATUS_BLOCK{}, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if status, ok := err.(windows.NTStatus); ok {
		return nil, status.Errno()
	}
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &info)
	if err == nil && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		err = errLink
	}
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}
