vice but altered by merry; original is here https://github.com/mmp/vice
====

![dall-e 2 tower](https://github.com/mmp/vice/blob/master/icons/tower-rounded-inset-256x256.png?raw=true)

*A fun folly writing an ATC simulator*. See the [vice
website](https://pharr.org/vice) for more information and documentation
about how to use vice.

[<img src="https://github.com/mmp/vice/actions/workflows/ci-windows.yml/badge.svg">](https://github.com/mmp/vice/actions?query=workflow%3Aci-windows)
[<img src="https://github.com/mmp/vice/actions/workflows/ci-mac.yml/badge.svg">](https://github.com/mmp/vice/actions?query=workflow%3Aci-mac)
[<img src="https://github.com/mmp/vice/actions/workflows/ci-linux.yml/badge.svg">](https://github.com/mmp/vice/actions?query=workflow%3Aci-linux)

# Building vice

To build *vice* from scratch, first make sure that you have a recent *go*
compiler installed: ([go compiler downloads](https://go.dev/dl/)).
Then clone the *vice* repository to a folder on your computer.

## Windows

To build *vice*, you must also have the *mingw64* compiler installed.  Make sure
that your `PATH` environment variable includes the *mingw64* `bin` directory.

Next, make sure that [SDL](https://www.libsdl.org) is installed on your
system. You may build it from source, though installing prebuilt binaries
is easier.  You can download [prebuilt binaries](https://github.com/libsdl-org/SDL/releases/download/release-2.24.0/SDL2-devel-2.24.0-mingw.zip)
from the [libsdl releases page](https://github.com/libsdl-org/SDL/releases/tag/release-2.24.0).
  
You will then need to set the following environment variables, with **INSTALL**
in the following replaced with the directory where you installed `SDL2-devel`:
  * `CGO_CFLAGS`: `'-I INSTALL/SDL2-2.24.0/x86_64-w64-mingw32/include'`
  * `CGO_CPPFLAGS`: `'-I INSTALL/SDL2-2.24.0/x86_64-w64-mingw32/include'`
  * `CGO_LDFLAGS`: `'-L INSTALL/SDL2-2.24.0/x86_64-w64-mingw32/lib'`

To build *vice*, run the command `go build -ldflags -H=windowsgui -o ./vice.exe . `
from a command shell in the repository directory.

## Mac OSX

If you have [homebrew](https://brew.sh) installed, running `brew
install sdl2` will install SDL2. Otherwise consult your package manager
documentation or install [SDL](https://www.libsdl.org) from source.

From a command shell in the repositoiry directory `go build -o vice` to
build a *vice* executable.

## Linux

On Ubuntu, `sudo apt install xorg-dev libsdl2-dev` will install the necessary libraries.
Then, from a command shell in the repositoiry directory `go build -o vice` to
build a *vice* executable.

## Release Builds

For *vice* releases, there are a few more steps in the build process so
that the executable has an icon and that OSX builds are universal binaries
that run on both Intel and Apple CPUs.  See the scripts in the
[osx](https://github.com/mmp/vice/tree/master/osx) and
[windows](https://github.com/mmp/vice/tree/master/windows) directories for
details.  See also the [github workflow for the Windows
build](https://github.com/mmp/vice/blob/master/.github/workflows/ci-windows.yml)
for details about how the Windows *vice* installer is created.
