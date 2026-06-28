// whisper_safe.cpp: C++ wrappers around whisper_init_* that translate
// C++ exceptions into a caller-supplied error buffer and a null return.
// Without these, a throw inside whisper.cpp surfaces as a "signal
// arrived during external code execution" fatal error in the Go
// runtime, which cannot be recovered via recover() and so leaves no
// trace in vice.slog or the public crash server.
//
// The error message is written into a caller-supplied buffer in the
// same call as the init, rather than stored in thread-local storage
// and fetched later, because the Go scheduler is free to reschedule a
// goroutine onto a different OS thread between cgo calls.

#include <whisper.h>

#include <cstring>
#include <exception>

namespace {

void copy_err(char* err_buf, size_t err_buf_size, const char* msg) {
    if (err_buf == nullptr || err_buf_size == 0) return;
    size_t n = std::strlen(msg);
    if (n >= err_buf_size) n = err_buf_size - 1;
    std::memcpy(err_buf, msg, n);
    err_buf[n] = '\0';
}

}  // namespace

extern "C" {

struct whisper_context* whisper_safe_init_from_buffer_with_params(
    void* buffer, size_t buffer_size, struct whisper_context_params params,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        return whisper_init_from_buffer_with_params(buffer, buffer_size, params);
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return nullptr;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return nullptr;
    }
}

struct whisper_context* whisper_safe_init_from_file_with_params(
    const char* path, struct whisper_context_params params,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        return whisper_init_from_file_with_params(path, params);
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return nullptr;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return nullptr;
    }
}

// whisper.cpp's Vulkan backend throws std::runtime_error from inference when
// GPU buffer allocation fails (seen in the wild on low-memory IGPs). Returns
// the underlying whisper_full_with_params_ptr status on success, or -1 with
// the exception message written to err_buf on a throw.
int whisper_safe_full_ptr(
    struct whisper_context* ctx, struct whisper_full_params* params,
    const float* samples, int n_samples,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        return whisper_full_with_params_ptr(ctx, params, samples, n_samples);
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return -1;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return -1;
    }
}

}  // extern "C"
