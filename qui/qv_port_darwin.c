//go:build cgo

/*
 * qv_port for macOS: POSIX mutexes, a condition variable waited on for a
 * length of time, and the main queue of the system to wake the window.
 */
#include <pthread.h>
#include <time.h>

#include "qv.h"

/* In qv_cocoa_darwin.m. */
void qv_cocoa_wake(void);
int  qv_cocoa_available(void);

static pthread_mutex_t locks[QV_LOCKS] = {PTHREAD_MUTEX_INITIALIZER, PTHREAD_MUTEX_INITIALIZER,
                                          PTHREAD_MUTEX_INITIALIZER};
static pthread_cond_t  ev_arrived = PTHREAD_COND_INITIALIZER;
static uint32_t        next_id;

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
    if (ms < 0) {
        pthread_cond_wait(&ev_arrived, &locks[QV_LOCK_EV]);
    } else {
        /* A length of time, not a moment: a change of the date cannot move it. */
        struct timespec span;
        span.tv_sec = ms / 1000;
        span.tv_nsec = (long)(ms % 1000) * 1000000L;
        pthread_cond_timedwait_relative_np(&ev_arrived, &locks[QV_LOCK_EV], &span);
    }
}

void qv_port_event_wake(void)
{
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
    qv_cocoa_wake();
}

int qv_win_available(void)
{
    return qv_cocoa_available();
}

int qv_win_has_tray(void)
{
    return 0;
}
