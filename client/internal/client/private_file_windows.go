package client

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func validatePrivateDirectory(info os.FileInfo) error { return nil }

func openPrivateFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func restrictPrivateFile(f *os.File) error {
	// Normal Go create handles lack WRITE_DAC. Reopen and verify the inode.
	secured, err := openPrivateFile(f.Name())
	if err != nil {
		return err
	}
	defer secured.Close()
	originalInfo, err := f.Stat()
	if err != nil {
		return err
	}
	securedInfo, err := secured.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(originalInfo, securedInfo) {
		return fmt.Errorf("private file changed before ACL update")
	}
	h := windows.Handle(secured.Fd())
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &details); err != nil {
		return err
	}
	if details.NumberOfLinks != 1 {
		return fmt.Errorf("private file must have exactly one link")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + owner.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}
