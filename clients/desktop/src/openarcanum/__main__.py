import sys

# Before anything heavy is imported: an untested interpreter fails in
# confusing ways (pyproject.toml requires-python).
if sys.version_info[:2] != (3, 14):
    sys.exit(f"OpenArcanum runs on Python 3.14; this is {sys.version.split()[0]}.")

from openarcanum.app import main

main()
