import unittest
from unittest.mock import patch, MagicMock
import commandcode_loopback_bridge as bridge

class TransportTests(unittest.TestCase):
    def test_endpoint(self):
        self.assertEqual(bridge.configure_endpoint("https://example.com/prefix/"),
            ("https", "example.com", None, "/prefix/v0/resource/plugins/commandcode/callback"))
    def test_reject_unsafe_urls(self):
        for url in ["http://example.com", "ftp://example.com", "https://u:p@example.com", "https://example.com?x=1", "https://example.com/#x"]:
            with self.assertRaises(ValueError): bridge.configure_endpoint(url)
    def test_explicit_http(self):
        self.assertEqual(bridge.configure_endpoint("http://localhost:8317", True)[:3], ("http", "localhost", 8317))
    def test_https_verified_default_and_redirect_rejected(self):
        with patch.object(bridge.http.client, "HTTPSConnection") as cls:
            conn=cls.return_value
            conn.getresponse.return_value.status=302
            self.assertFalse(bridge.forward_callback(bridge.configure_endpoint("https://example.com"), {"X-CommandCode-State":"test"}))
            cls.assert_called_once_with("example.com", None, timeout=20)
            conn.request.assert_called_once()
            conn.close.assert_called_once()
    def test_http_success(self):
        with patch.object(bridge.http.client, "HTTPConnection") as cls:
            cls.return_value.getresponse.return_value.status=200
            self.assertTrue(bridge.forward_callback(bridge.configure_endpoint("http://localhost",True), {}))

if __name__ == "__main__": unittest.main()
