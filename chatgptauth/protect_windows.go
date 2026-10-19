package chatgptauth

import (
	"os"

	"golang.org/x/sys/windows"
)

// Unix permission bits do not restrict Windows readers. Remove inherited grants
// and allow only this Windows user and SYSTEM to access the credential store.
func privateDACL() (*windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	return acl, err
}

func protectDirectory(path string) error {
	acl, err := privateDACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func protectFile(file *os.File) error {
	acl, err := privateDACL()
	if err != nil {
		return err
	}
	// os.Open does not request WRITE_DAC. The directory has already been made
	// private before opening or creating files in it.
	return windows.SetNamedSecurityInfo(file.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
