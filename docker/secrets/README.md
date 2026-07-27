# Docker secrets

`one-admin-password.txt` is intentionally ignored by Git. It is consumed only
on first start to create the initial administrator and is never copied into the
image.

The password must contain uppercase and lowercase letters, a number, and a
symbol, and must not contain common weak-password words.
