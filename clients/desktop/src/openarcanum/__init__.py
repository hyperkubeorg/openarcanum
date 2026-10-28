"""OpenArcanum desktop client (pywebview)."""

# Names the OS app directories and the window title. Code that needs the
# product name reads it from here, which keeps a rename cheap.
PRODUCT_NAME = "OpenArcanum"
__version__ = "2026.10.10"


def main() -> None:
    from openarcanum.app import main as app_main

    app_main()
