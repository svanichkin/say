//go:build !darwin && !linux && !windows

package contacts

func getMe(path string) (string, bool) { return "", false }
func setMe(path, uid string) error     { return errNoAttr }
func clearMe(path string) error        { return nil }
