vice
====

![dall-e 2 tower](https://github.com/mmp/vice/blob/master/icons/tower-rounded-inset-256x256.png?raw=true)

*A fun folly writing an ATC client*.

[<img src="https://github.com/mmp/vice/actions/workflows/ci-windows.yml/badge.svg">](https://github.com/mmp/vice/actions?query=workflow%3Aci-windows)
[<img src="https://github.com/mmp/vice/actions/workflows/ci-mac.yml/badge.svg">](https://github.com/mmp/vice/actions?query=workflow%3Aci-mac)
[<img src="https://github.com/mmp/vice/actions/workflows/ci-linux.yml/badge.svg">](https://github.com/mmp/vice/actions?query=workflow%3Aci-linux)

Building vice
-------------

To build *vice* from scratch, first make sure that you have a recent *go*
compiler installed: ([go compiler downloads](https://go.dev/dl/)).

Next, make sure that [SDL](https://www.libsdl.org) is installed on your
system. You may build it from source, though installing prebuilt binaries
is easier:
* Windows: You can download [prebuilt
  binaries](https://github.com/libsdl-org/SDL/releases/download/release-2.24.0/SDL2-devel-2.24.0-mingw.zip)
  from the [libsdl releases
  page](https://github.com/libsdl-org/SDL/releases/tag/release-2.24.0). You
  will then need to set the following environment variables, with **INSTALL**
  in the following replaced with the directory where you installed `SDL2-devel`:
  * `CGO_CFLAGS`: `'-I INSTALL/SDL2-2.24.0/x86_64-w64-mingw32/include'`
  * `CGO_CPPFLAGS`: `'-I INSTALL/SDL2-2.24.0/x86_64-w64-mingw32/include'`
  * `CGO_LDFLAGS`: `'-L INSTALL/SDL2-2.24.0/x86_64-w64-mingw32/lib'`
* OSX: If you have [homebrew](https://brew.sh) installed, running `brew
  install sdl2` will install SDL2.
* Linux: On Ubuntu, `sudo apt install xorg-dev libsdl2-dev` will install
  the necessary libraries.

On Windows, you must also have the *mingw64* compiler installed.  Make sure
that your `PATH` environment variable includes the *mingw64* `bin` directory.

With all of that set up, run the following command in a shell:
```
go install github.com/mmp/vice@latest
```

When the build completes, a *vice* binary will be in your
`${GOPATH}/bin/vice` directory; if the `GOPATH` environment variable is
unset then *vice* will be in `go/bin/vice`, where the `go/` directory is in
your home directory.

For *vice* releases, there are a few more steps in the build process so
that the executable has an icon and that OSX builds are universal binaries
that run on both Intel and Apple CPUs.  See the scripts in the
[osx](https://github.com/mmp/vice/tree/master/osx) and
[windows](https://github.com/mmp/vice/tree/master/windows) directories for
details.  See also the [github workflow for the Windows
build](https://github.com/mmp/vice/blob/master/.github/workflows/ci-windows.yml)
for details about how the Windows *vice* installer is created.
