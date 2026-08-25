from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]
CLIENT = ROOT / "packaging" / "client"


class DesktopPackagingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.launcher = (CLIENT / "start-osanwe.ps1").read_text(encoding="utf-8")
        cls.installer = (CLIENT / "osanwe.iss").read_text(encoding="utf-8")
        cls.release = (ROOT / ".github" / "workflows" / "release.yml").read_text(
            encoding="utf-8"
        )

    def test_windows_launcher_owns_a_clean_client_lifetime(self):
        for required in (
            'RedirectStandardInput = $true',
            "StandardInput.Close()",
            "-exit-on-stdin-close",
            "--app=",
            "--disable-background-mode",
        ):
            with self.subTest(required=required):
                self.assertIn(required, self.launcher)

    def test_windows_launcher_keeps_credentials_out_of_arguments_and_disk(self):
        self.assertIn('EnvironmentVariables["OSANWE_SECRET"]', self.launcher)
        self.assertIn('EnvironmentVariables["OSANWE_RECEIPT"]', self.launcher)
        self.assertIn("UseSystemPasswordChar = $true", self.launcher)
        self.assertNotRegex(
            self.launcher,
            re.compile(r"Arguments\s*=.*OSANWE_(?:SECRET|RECEIPT)", re.IGNORECASE),
        )
        self.assertNotRegex(
            self.launcher,
            re.compile(r"(?:Set-Content|Out-File).*\$(?:secret|receipt)", re.IGNORECASE),
        )

    def test_enrollment_import_rejects_certificate_path_escape(self):
        self.assertIn("Resolve-ContainedPath", self.launcher)
        self.assertIn("StartsWith($parentFull", self.launcher)
        self.assertIn("OrdinalIgnoreCase", self.launcher)

    def test_installer_is_per_user_reopenable_and_uninstallable(self):
        for required in (
            "PrivilegesRequired=lowest",
            r"DefaultDirName={localappdata}\Programs\Osanwe",
            r'Name: "{group}\Osanwe"',
            r'Name: "{autodesktop}\Osanwe"',
            r'Name: "{group}\Uninstall Osanwe"',
            "AppMutex=Local\\OsanweDesktop",
        ):
            with self.subTest(required=required):
                self.assertIn(required, self.installer)

    def test_release_builds_and_checksums_the_installer(self):
        self.assertIn("windows-installer:", self.release)
        self.assertIn("Osanwe-Setup_*.exe", self.release)
        self.assertIn("sha256sum osanwe-client_* Osanwe-Setup_*.exe", self.release)
        self.assertIn("smoke install Windows app", self.release)
        self.assertIn("'/VERYSILENT'", self.release)
        self.assertIn("'unins000.exe'", self.release)


if __name__ == "__main__":
    unittest.main()
