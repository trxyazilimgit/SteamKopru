#!/bin/sh
# Builds SteamKopru.exe with LLVM clang (MSVC target) and marks it as a GUI app.
set -e
cd "$(dirname "$0")"
# exe icon resource (regenerate after changing the icon: go run ./tools/icongen)
[ -f rsrc_windows_amd64.syso ] || go run ./tools/icongen
PATH="/c/Program Files/LLVM/bin:$PATH" CC=clang CGO_ENABLED=1 \
CGO_CFLAGS="-O2 -DWINBOOL=BOOL" \
CGO_LDFLAGS="-luser32 -lgdi32 -lshell32 -lopengl32 -lole32 -ladvapi32 -lwinmm -limm32 -ldwmapi" \
go build -trimpath -ldflags "-s -w" -o SteamKopru.exe . 2>build.log || { grep -v "section name" build.log; exit 1; }
python -I -c "
import struct;f=open('SteamKopru.exe','r+b');d=f.read(0x400);pe=struct.unpack_from('<I',d,0x3c)[0];f.seek(pe+24+68);f.write(struct.pack('<H',2))"
echo built
