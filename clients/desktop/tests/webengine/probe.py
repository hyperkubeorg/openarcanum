"""Engine capability probe for the call media path: it loads probe.html in a
bare QWebEngineView (the engine the client ships, over file:// like the
app) and reports whether getUserMedia (with echo cancellation, noise
suppression and gain control), getDisplayMedia, encoded transforms and the
needed codecs work.
test_webengine.py runs it with Chromium's fake-device flags; CI runs it on
Linux, Windows and macOS. Prints one JSON object."""

import json
import sys
from pathlib import Path

from PySide6.QtCore import QTimer, QUrl
from PySide6.QtWebEngineWidgets import QWebEngineView
from PySide6.QtWidgets import QApplication


def main() -> None:
    app = QApplication(sys.argv)
    view = QWebEngineView()
    page = view.page()
    # The app grants media permissions the same way (only for its own page).
    page.permissionRequested.connect(lambda p: p.grant())
    page.desktopMediaRequested.connect(lambda req: req.selectScreen(req.screensModel().index(0, 0)))
    view.load(QUrl.fromLocalFile(str(Path(__file__).with_name("probe.html"))))
    view.show()

    def check(v):
        d = json.loads(v or "{}")
        if d.get("done"):
            print(json.dumps(d))
            app.quit()

    timer = QTimer()
    timer.timeout.connect(lambda: page.runJavaScript("JSON.stringify(window.result || {})", 0, check))
    timer.start(500)
    # The page waits up to 20 s for frames (probe.html), then asks for a screen.
    QTimer.singleShot(60000, lambda: (print(json.dumps({"timeout": True})), app.quit()))
    app.exec()


if __name__ == "__main__":
    main()
