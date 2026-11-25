//go:build cgo && !quevu_nogui

/*
 * qv_port for Linux: POSIX mutexes, a condition variable on the clock
 * that only goes forward, and the main loop of GLib to wake the window.
 */
#include <errno.h>
#include <pthread.h>
#include <time.h>

#include "qv.h"
#include "qv_gtk_linux.h"

static pthread_mutex_t locks[QV_LOCKS] = {PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER,
                                          PTHREAD_MUTEX_INITIALIZER};
static pthread_cond_t  ev_arrived;
static pthread_once_t  once = PTHREAD_ONCE_INIT;
static uint32_t        next_id;

static void setup(void)
{
    /* Waits are measured on the clock that a change of the date cannot move. */
    pthread_condattr_t attr;
    pthread_condattr_init(&attr);
    pthread_condattr_setclock(&attr, CLOCK_MONOTONIC);
    pthread_cond_init(&ev_arrived, &attr);
    pthread_condattr_destroy(&attr);
}

void qv_port_lock(int which)
{
    pthread_mutex_lock(&locks[which]);
}

void qv_port_unlock(int which)
{
    pthread_mutex_unlock(&locks[which]);
}

void qv_port_event_wait(int32_t ms)
{
    pthread_once(&once, setup);
    if (ms < 0) {
        pthread_cond_wait(&ev_arrived, &locks[QV_LOCK_EV]);
    } else {
        struct timespec until;
        clock_gettime(CLOCK_MONOTONIC, &until);
        until.tv_sec += ms / 1000;
        until.tv_nsec += (long)(ms % 1000) * 1000000L;
        if (until.tv_nsec >= 1000000000L) {
            until.tv_sec++;
            until.tv_nsec -= 1000000000L;
        }
        pthread_cond_timedwait(&ev_arrived, &locks[QV_LOCK_EV], &until);
    }
}

void qv_port_event_wake(void)
{
    pthread_once(&once, setup);
    pthread_cond_broadcast(&ev_arrived);
}

uint64_t qv_port_ms(void)
{
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    return (uint64_t)now.tv_sec * 1000u + (uint64_t)(now.tv_nsec / 1000000L);
}

uint32_t qv_port_next_id(void)
{
    return __sync_add_and_fetch(&next_id, 1);
}

void qv_port_wake(void *window)
{
    (void)window;
    qv_gtk_wake();
}

int qv_win_available(void)
{
    return qv_gtk_available();
}

int qv_win_has_tray(void)
{
    return 0;
}
