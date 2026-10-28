# PHP release verification key

`php-8.4.asc` is the public key of PHP 8.4 release manager Calvin Buckley.
It contains no private material.

- Full pinned fingerprint: `9D7F99A0CB8F05C8A6958D6256A97AF7600A39A6`.
- Fingerprint checked against https://www.php.net/downloads.php?source=Y on 2026-10-28.
- Key obtained from https://www.php.net/distributions/php-keyring.gpg and exported
  by its full fingerprint from an isolated temporary GnuPG home.
- Exported armored file SHA-256:
  `0c5b0930d7a91e773152da13a4bfe05f4582fde68dcf155ed6e230ded9a2e5b7`.

The initial PHP 8.4.27 archive is signed by this key. A later release signed by
another release manager must undergo an explicit key/recipe update. There is no
automatic keyserver trust or signature-verification bypass. A valid source
signature does not sign or endorse the resulting PHPM binary packages.

## PHP 8.5

`php-8.5.asc` is Daniel Scherzer's public PHP release key, obtained from the same
official PHP keyring in an isolated temporary home. The full fingerprint
`D95C03BC702BE9515344AE3374E44BC9067701A5` was checked against the PHP downloads
page on 2026-10-28 and the 8.5.12 detached signature's issuer. Exported key SHA-256:
`f095daa40ea38692bcfb04b711bd98a346238fb2b1da6c4f0207c8cbdfc48b38`.
The recipe pins the official 8.5.12 tar.xz SHA-256 published by
https://www.php.net/releases/index.php?json&version=8.5.12 and still requires full
archive/signature verification before compilation.
