package main

// signet set-default: make Signet the app that opens PDFs on this computer.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func setDefault() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return setDefaultMac(exe)
	case "windows":
		return setDefaultWindows(exe)
	default:
		return setDefaultLinux(exe)
	}
}

func setDefaultLinux(exe string) error {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Signet\nComment=Fill, sign and initial PDFs\nExec=%q open %%F\nMimeType=application/pdf;\nTerminal=false\nCategories=Office;\n", exe)
	if err := os.WriteFile(filepath.Join(dir, "signet.desktop"), []byte(desktop), 0o644); err != nil {
		return err
	}
	exec.Command("update-desktop-database", dir).Run()
	if out, err := exec.Command("xdg-mime", "default", "signet.desktop", "application/pdf").CombinedOutput(); err != nil {
		return fmt.Errorf("added Signet to your apps, but xdg-mime failed (%v: %s); pick Signet in your file manager's Open With", err, out)
	}
	fmt.Println("Done: PDFs now open in Signet.")
	return nil
}

func setDefaultWindows(exe string) error {
	cmd := fmt.Sprintf(`"%s" open "%%1"`, exe)
	steps := [][]string{
		{"add", `HKCU\Software\Classes\Signet.PDF`, "/ve", "/d", "PDF document", "/f"},
		{"add", `HKCU\Software\Classes\Signet.PDF\shell\open\command`, "/ve", "/d", cmd, "/f"},
		{"add", `HKCU\Software\Classes\Applications\signet.exe`, "/v", "FriendlyAppName", "/d", "Signet", "/f"},
		{"add", `HKCU\Software\Classes\Applications\signet.exe\shell\open\command`, "/ve", "/d", cmd, "/f"},
		{"add", `HKCU\Software\Classes\.pdf\OpenWithProgids`, "/v", "Signet.PDF", "/t", "REG_NONE", "/f"},
	}
	for _, s := range steps {
		if out, err := exec.Command("reg", s...).CombinedOutput(); err != nil {
			return fmt.Errorf("reg %s: %v: %s", strings.Join(s, " "), err, out)
		}
	}
	fmt.Println("Signet is now in Open With. Windows asks you to choose the default yourself, once:")
	fmt.Println("right-click any PDF > Open with > Choose another app > Signet > Always.")
	return nil
}

// On a Mac, apps get files through Apple Events, not arguments, so Signet makes
// a tiny AppleScript app (~/Applications/Signet.app) that hands them to this binary.
func setDefaultMac(exe string) error {
	home, _ := os.UserHomeDir()
	app := filepath.Join(home, "Applications", "Signet.app")
	q := strings.ReplaceAll(exe, `"`, `\"`)
	script := fmt.Sprintf(`on open theFiles
	set args to ""
	repeat with f in theFiles
		set args to args & " " & quoted form of POSIX path of f
	end repeat
	do shell script quoted form of "%s" & " open" & args & " > /dev/null 2>&1 &"
end open
on run
	do shell script quoted form of "%s" & " open > /dev/null 2>&1 &"
end run
`, q, q)
	src := filepath.Join(os.TempDir(), "signet-app.applescript")
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		return err
	}
	defer os.Remove(src)
	os.MkdirAll(filepath.Dir(app), 0o755)
	os.RemoveAll(app)
	if out, err := exec.Command("osacompile", "-o", app, src).CombinedOutput(); err != nil {
		return fmt.Errorf("osacompile: %v: %s", err, out)
	}
	plist := filepath.Join(app, "Contents", "Info.plist")
	for _, c := range []string{
		"Add :CFBundleIdentifier string dev.signet.app", "Set :CFBundleIdentifier dev.signet.app",
		"Set :CFBundleName Signet",
		"Add :CFBundleDocumentTypes array",
		"Add :CFBundleDocumentTypes:0 dict",
		"Add :CFBundleDocumentTypes:0:CFBundleTypeName string PDF",
		"Add :CFBundleDocumentTypes:0:CFBundleTypeRole string Editor",
		"Add :CFBundleDocumentTypes:0:LSHandlerRank string Alternate",
		"Add :CFBundleDocumentTypes:0:LSItemContentTypes array",
		"Add :CFBundleDocumentTypes:0:LSItemContentTypes:0 string com.adobe.pdf",
	} {
		exec.Command("/usr/libexec/PlistBuddy", "-c", c, plist).Run()
	}
	exec.Command("codesign", "--force", "-s", "-", app).Run()
	exec.Command("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", "-f", app).Run()
	// Ask, then check: macOS applies the change a few seconds later, and newer
	// versions may ignore it, so only say "done" once it has really happened.
	jxa := `ObjC.import("CoreServices");
var want = "dev.signet.app", now = "";
for (var i = 0; i < 40 && now !== want; i++) {
  if (i % 8 === 0) $.LSSetDefaultRoleHandlerForContentType($("com.adobe.pdf"), 0xFFFFFFFF, $(want));
  delay(0.25);
  var h = $.LSCopyDefaultRoleHandlerForContentType($("com.adobe.pdf"), 0xFFFFFFFF);
  now = h ? ObjC.castRefToObject(h).js : "";
}
now;`
	out, err := exec.Command("osascript", "-l", "JavaScript", "-e", jxa).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "dev.signet.app" {
		fmt.Printf("Made %s, but macOS did not let Signet make itself the default.\n", app)
		fmt.Println("Do it once by hand: right-click a PDF > Get Info > Open with: Signet > Change All.")
		return nil
	}
	fmt.Printf("Done: PDFs now open in Signet (%s).\n", app)
	return nil
}
