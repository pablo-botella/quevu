/*
 * qv_port for Windows: slim locks, a condition variable, and a posted
 * message to wake the window.
 */
#include <windows.h>

#include "qv.h"
#include "qv_win.h"

static SRWLOCK            locks[QV_LOCKS] = {SRWLOCK_INIT, SRWLOCK_INIT, SRWLOCK_INIT};
static CONDITION_VARIABLE ev_arrived = CONDITION_VARIABLE_INIT;
static volatile LONG      next_id;

void qv_port_lock(int which)
{
    AcquireSRWLockExclusive(&locks[which]);
}

void qv_port_unlock(int which)
{
    ReleaseSRWLockExclusive(&locks[which]);
}

void qv_port_event_wait(int32_t ms)
{
    SleepConditionVariableSRW(&ev_arrived, &locks[QV_LOCK_EV], ms < 0 ? INFINITE : (DWORD)ms, 0);
}

void qv_port_event_wake(void)
{
    WakeAllConditionVariable(&ev_arrived);
}

uint64_t qv_port_ms(void)
{
    return GetTickCount64();
}

uint32_t qv_port_next_id(void)
{
    return (uint32_t)InterlockedIncrement(&next_id);
}

void qv_port_wake(void *window)
{
    /*
     * A handle is just a number as far as posting a message goes, which
     * is what lets this cross threads.
     */
    PostMessageW((HWND)window, WM_QV_WAKE, 0, 0);
}

int qv_win_available(void)
{
    return 1;
}

int qv_win_has_tray(void)
{
    return 1;
}
