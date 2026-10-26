"""Entry point of the bundled app: PyInstaller runs this file as __main__
(packaging/openarcanum.spec). Same as `python -m openarcanum`."""

from openarcanum.app import main

if __name__ == "__main__":
    main()
