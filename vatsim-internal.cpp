// vatsim-internal.cpp
// Copyright(c) 2022 Matt Pharr.

#include "vatsim-internal.h"

#if defined(__APPLE__)

#include <unistd.h>
#include <uuid/uuid.h>

const char *getSysuid() {
    struct timespec wait;
    wait.tv_sec = 5;
    wait.tv_nsec = 0;

    uuid_t id;
    if (gethostuuid(id, &wait) == -1) {
        return "(error)";
    }

    static char buf[40];
    uuid_unparse_lower(id, buf);

    return buf;
}

#elif defined(_WIN32)

#error "TODO getSysuid for windows"

#else

// Assume linux

#include <stdio.h>
#include <systemd/sd-id128.h>

#define VICE_ID SD_ID128_MAKE(ad,8e,c7,ff,0a,69,44,97,97,c3,fe,1e,20,65,3a,4d)

const char *getSysuid() {
  sd_id128_t id;
  sd_id128_get_machine_app_specific(VICE_ID, &id);

  static char buf[33];
  for (i = 0; i < 16; ++i)
      sprintf(buf+2*i, "%02x", id.bytes[i]);
  return buf;
}

#endif
