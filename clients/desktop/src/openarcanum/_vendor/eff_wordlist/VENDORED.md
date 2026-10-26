# Vendored: EFF large wordlist for passphrases

The word list `openarcanum.passgen` draws generated passphrases from (the
**Generate** button next to every new-passphrase field). Why this list and
why six words: `docs/user-guide.md` "Generate a passphrase".

## Upstream

| | |
|---|---|
| Work | "EFF's Long Wordlist" (the large list for five dice), by Joseph Bonneau, Electronic Frontier Foundation, 2016 |
| Page | https://www.eff.org/deeplinks/2016/07/new-wordlists-random-passphrases |
| File | https://www.eff.org/files/2016/07/18/eff_large_wordlist.txt |
| sha256 | `addd35536511597a02fa0a9ff1e5284677b8883b83e986e43f15a3db996b903e` (checked by `openarcanum.passgen` every time it loads the list) |
| Size | 7776 lines, `<five dice digits>\t<word>`; 7776 distinct words of 3 to 9 lowercase characters (four have a hyphen: drop-down, felt-tip, t-shirt, yo-yo, so generated passphrases separate words with spaces), none a prefix of another |
| License | Creative Commons Attribution 3.0 United States (CC BY 3.0 US), https://creativecommons.org/licenses/by/3.0/us/ |

## Attribution

"EFF's Long Wordlist" by the Electronic Frontier Foundation
(https://www.eff.org/dice), used under CC BY 3.0 US. The file is
byte-for-byte the published one; OpenArcanum uses the words only, not the dice
numbers. The attribution is also shown in Settings → About.

## Local changes

None.
