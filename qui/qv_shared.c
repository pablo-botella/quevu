//go:build cgo && (windows || darwin || (linux && !quevu_nogui))

/*
 * qv_shared - the two one-way lanes between the host and the thread of
 * the window, and the functions of quevu_ui.h, which do nothing but put
 * a command on the first lane or read the second.
 *
 * The host talks to the window from any thread; the window lives on one.
 * They never call each other: a command is queued and the window woken,
 * an event is queued and whoever waits in quevu_ui_next_event is woken.
 *
 * Nothing here knows a system: the locks, the waiting and the way a
 * window is woken come from qv_port (see qv.h).
 */
#include <stdlib.h>
#include <string.h>

#include "qv.h"

typedef struct ev_node {
    struct ev_node *next;
    quevu_ui_event  ev;
} ev_node;

typedef struct find_node {
    struct find_node *next;
    uint32_t tab;
    char    *text;
    int64_t  since_ns, until_ns;
} find_node;

/* One per process, like the window. */
static qv_cmd *cmd_head, *cmd_tail;
static void   *waker; /* the window to wake, or null: under QV_LOCK_CMD */

static ev_node *ev_head, *ev_tail; /* under QV_LOCK_EV */
static int      ev_ended;

static find_node *finds; /* under QV_LOCK_FIND */

uint32_t qv_new_id(void)
{
    /* Never zero, never repeated. */
    return qv_port_next_id();
}

void qv_post(qv_cmd *c)
{
    c->next = NULL;
    qv_port_lock(QV_LOCK_CMD);
    if (cmd_tail)
        cmd_tail->next = c;
    else
        cmd_head = c;
    cmd_tail = c;
    /* Commands posted before the loop starts wait for it. */
    if (waker)
        qv_port_wake(waker);
    qv_port_unlock(QV_LOCK_CMD);
}

qv_cmd *qv_drain(void)
{
    qv_cmd *all;
    qv_port_lock(QV_LOCK_CMD);
    all = cmd_head;
    cmd_head = cmd_tail = NULL;
    qv_port_unlock(QV_LOCK_CMD);
    return all;
}

void qv_begin(void)
{
    /*
     * Events can be emitted from here on, before the loop is up, and the
     * end a window before this one left behind no longer counts.
     */
    qv_port_lock(QV_LOCK_EV);
    ev_ended = 0;
    qv_port_unlock(QV_LOCK_EV);
}

void qv_attach(void *window)
{
    qv_begin();
    qv_port_lock(QV_LOCK_CMD);
    waker = window;
    qv_port_unlock(QV_LOCK_CMD);
}

void qv_detach(void)
{
    qv_port_lock(QV_LOCK_CMD);
    waker = NULL;
    qv_port_unlock(QV_LOCK_CMD);
    qv_port_lock(QV_LOCK_EV);
    ev_ended = 1;
    qv_port_unlock(QV_LOCK_EV);
    qv_port_event_wake();
}

void qv_emit(uint32_t kind, uint32_t id, uint32_t op, uint32_t n, uint64_t seq)
{
    ev_node *e = qv_alloc(sizeof *e);
    e->ev.kind = kind;
    e->ev.id = id;
    e->ev.op = op;
    e->ev.n = n;
    e->ev.seq = seq;
    qv_port_lock(QV_LOCK_EV);
    if (ev_tail)
        ev_tail->next = e;
    else
        ev_head = e;
    ev_tail = e;
    qv_port_unlock(QV_LOCK_EV);
    qv_port_event_wake();
}

int qv_next_event(quevu_ui_event *ev, int32_t timeout_ms)
{
    uint64_t deadline = timeout_ms >= 0 ? qv_port_ms() + (uint64_t)timeout_ms : 0;
    int      result;

    qv_port_lock(QV_LOCK_EV);
    for (;;) {
        if (ev_head) {
            ev_node *e = ev_head;
            ev_head = e->next;
            if (!ev_head)
                ev_tail = NULL;
            if (ev)
                *ev = e->ev;
            free(e);
            result = 1;
            break;
        }
        /* The end of the loop is seen after the events still queued. */
        if (ev_ended) {
            result = -1;
            break;
        }
        if (timeout_ms < 0) {
            qv_port_event_wait(-1);
        } else {
            uint64_t now = qv_port_ms();
            if (now >= deadline) {
                result = 0;
                break;
            }
            qv_port_event_wait((int32_t)(deadline - now));
        }
    }
    qv_port_unlock(QV_LOCK_EV);
    return result;
}

/*
 * Events are fixed in size, so the text a console is looking for waits
 * here for the host to read it.
 */
void qv_set_find(uint32_t tab, char *text, int64_t since_ns, int64_t until_ns)
{
    find_node *f;
    qv_port_lock(QV_LOCK_FIND);
    for (f = finds; f; f = f->next)
        if (f->tab == tab)
            break;
    if (!f) {
        f = qv_alloc(sizeof *f);
        f->tab = tab;
        f->next = finds;
        finds = f;
    }
    free(f->text);
    f->text = text;
    f->since_ns = since_ns;
    f->until_ns = until_ns;
    qv_port_unlock(QV_LOCK_FIND);
}

uint32_t qv_find_text(uint32_t tab, char *buf, uint32_t cap)
{
    find_node *f;
    size_t     len = 0;

    qv_port_lock(QV_LOCK_FIND);
    for (f = finds; f; f = f->next)
        if (f->tab == tab)
            break;
    if (f && f->text)
        len = strlen(f->text);
    if (buf && cap > 0) {
        size_t n = len < (size_t)cap - 1 ? len : (size_t)cap - 1;
        if (n)
            memcpy(buf, f->text, n);
        buf[n] = 0;
    }
    qv_port_unlock(QV_LOCK_FIND);
    return (uint32_t)len;
}

void qv_find_range(uint32_t tab, int64_t *since_ns, int64_t *until_ns)
{
    find_node *f;
    qv_port_lock(QV_LOCK_FIND);
    for (f = finds; f; f = f->next)
        if (f->tab == tab)
            break;
    if (since_ns)
        *since_ns = f ? f->since_ns : 0;
    if (until_ns)
        *until_ns = f ? f->until_ns : 0;
    qv_port_unlock(QV_LOCK_FIND);
}

/* ---- quevu_ui.h ---- */

static void verbs_from(qv_verbs *out, const quevu_ui_verb *verbs, uint32_t n)
{
    uint32_t i;
    if (!verbs || n == 0)
        return;
    out->v = qv_alloc(n * sizeof *out->v);
    out->n = n;
    for (i = 0; i < n; i++) {
        out->v[i].id = verbs[i].id;
        out->v[i].attrs = verbs[i].attrs;
        out->v[i].label = qv_utf8_to_wide(verbs[i].label);
    }
}

static void lines_from(qv_cmd *c, const quevu_ui_chunk *chunks, uint32_t n)
{
    uint32_t i;
    if (!chunks || n == 0)
        return;
    c->lines = qv_alloc(n * sizeof *c->lines);
    c->nlines = n;
    for (i = 0; i < n; i++) {
        c->lines[i].seq = chunks[i].seq;
        c->lines[i].time_ns = chunks[i].time_ns;
        qv_display_line(chunks[i].data, chunks[i].data ? chunks[i].len : 0, &c->lines[i]);
    }
}

static void post_text(int kind, uint32_t id, const char *text)
{
    qv_cmd *c = qv_cmd_new(kind);
    c->id = id;
    c->text = qv_utf8_to_wide(text);
    qv_post(c);
}

static void post_id(int kind, uint32_t id)
{
    qv_cmd *c = qv_cmd_new(kind);
    c->id = id;
    qv_post(c);
}

static void post_flag(int kind, uint32_t id, int on)
{
    qv_cmd *c = qv_cmd_new(kind);
    c->id = id;
    c->full = on;
    qv_post(c);
}

uint32_t quevu_ui_abi(void)
{
    return QUEVU_UI_ABI;
}

int32_t quevu_ui_run(const char *title)
{
    uint16_t *w = qv_utf8_to_wide(title);
    int       code = qv_win_run(w);
    free(w);
    return code;
}

void quevu_ui_quit(void)
{
    qv_post(qv_cmd_new(QV_QUIT));
}

void quevu_ui_text(uint32_t id, const char *text)
{
    post_text(QV_TEXT, id, text);
}

static uint32_t tab_add(int kind, const char *title, int32_t weight)
{
    qv_cmd *c = qv_cmd_new(kind);
    uint32_t id = qv_new_id();
    c->id = id;
    c->text = qv_utf8_to_wide(title);
    c->weight = weight;
    qv_post(c);
    return id;
}

uint32_t quevu_ui_console_add(const char *title, int32_t weight)
{
    return tab_add(QV_CONSOLE_ADD, title, weight);
}

uint32_t quevu_ui_controls_add(const char *title, int32_t weight)
{
    return tab_add(QV_CONTROLS_ADD, title, weight);
}

void quevu_ui_tab_title(uint32_t tab, const char *title)
{
    post_text(QV_TAB_TITLE, tab, title);
}

void quevu_ui_tab_select(uint32_t tab)
{
    post_id(QV_TAB_SELECT, tab);
}

void quevu_ui_tab_remove(uint32_t tab)
{
    post_id(QV_TAB_REMOVE, tab);
}

void quevu_ui_tab_note(uint32_t tab, const char *note)
{
    post_text(QV_TAB_NOTE, tab, note);
}

void quevu_ui_tab_verbs(uint32_t tab, const quevu_ui_verb *verbs, uint32_t n)
{
    qv_cmd *c = qv_cmd_new(QV_TAB_VERBS);
    c->id = tab;
    verbs_from(&c->verbs, verbs, n);
    qv_post(c);
}

uint32_t quevu_ui_control_add(uint32_t tab, const char *name)
{
    qv_cmd *c = qv_cmd_new(QV_CONTROL_ADD);
    uint32_t id = qv_new_id();
    c->id = id;
    c->tab = tab;
    c->text = qv_utf8_to_wide(name);
    qv_post(c);
    return id;
}

void quevu_ui_control_state(uint32_t control, uint32_t rgb, uint32_t attrs,
                            const char *state, const quevu_ui_verb *verbs,
                            uint32_t n)
{
    qv_cmd *c = qv_cmd_new(QV_CONTROL_STATE);
    c->id = control;
    c->a = rgb;
    c->b = attrs;
    c->text = qv_utf8_to_wide(state);
    verbs_from(&c->verbs, verbs, n);
    qv_post(c);
}

void quevu_ui_control_remove(uint32_t control)
{
    post_id(QV_CONTROL_REMOVE, control);
}

void quevu_ui_console_deliver(uint32_t tab, uint32_t op, uint32_t flags,
                              const quevu_ui_chunk *chunks, uint32_t n)
{
    qv_cmd *c = qv_cmd_new(QV_DELIVER);
    c->id = tab;
    c->a = op;
    c->full = (flags & QUEVU_FLAG_FULL) != 0;
    c->history = (flags & QUEVU_FLAG_HISTORY) != 0;
    lines_from(c, chunks, n);
    qv_post(c);
}

void quevu_ui_console_time(uint32_t tab, uint32_t on)
{
    post_flag(QV_CONSOLE_TIME, tab, on != 0);
}

uint32_t quevu_ui_console_find_text(uint32_t tab, char *buf, uint32_t cap)
{
    return qv_find_text(tab, buf, cap);
}

void quevu_ui_console_find_range(uint32_t tab, int64_t *since_ns, int64_t *until_ns)
{
    qv_find_range(tab, since_ns, until_ns);
}

void quevu_ui_console_goto(uint32_t tab, const quevu_ui_chunk *chunks,
                           uint32_t n, uint64_t mark, uint32_t flags)
{
    qv_cmd *c = qv_cmd_new(QV_GOTO);
    c->id = tab;
    c->seq = mark;
    c->history = (flags & QUEVU_FLAG_HISTORY) != 0;
    lines_from(c, chunks, n);
    qv_post(c);
}

void quevu_ui_console_find_status(uint32_t tab, const char *status,
                                  uint64_t resume, uint32_t flags)
{
    qv_cmd *c = qv_cmd_new(QV_FIND_STATUS);
    c->id = tab;
    c->text = qv_utf8_to_wide(status);
    c->seq = resume;
    c->history = (flags & QUEVU_FLAG_HISTORY) != 0;
    qv_post(c);
}

uint32_t quevu_ui_menu_add(uint32_t parent, const char *label, uint32_t attrs)
{
    qv_cmd *c = qv_cmd_new(QV_MENU_ADD);
    uint32_t id = qv_new_id();
    c->id = id;
    c->tab = parent;
    c->text = qv_utf8_to_wide(label);
    c->b = attrs;
    qv_post(c);
    return id;
}

void quevu_ui_menu_set(uint32_t item, const char *label, uint32_t attrs)
{
    qv_cmd *c = qv_cmd_new(QV_MENU_SET);
    c->id = item;
    c->text = qv_utf8_to_wide(label);
    c->b = attrs;
    qv_post(c);
}

void quevu_ui_menu_remove(uint32_t item)
{
    post_id(QV_MENU_REMOVE, item);
}

void quevu_ui_tray(uint32_t enabled)
{
    post_flag(QV_TRAY, 0, enabled != 0);
}

void quevu_ui_show(uint32_t visible)
{
    post_flag(QV_SHOW, 0, visible != 0);
}

int32_t quevu_ui_next_event(quevu_ui_event *ev, int32_t timeout_ms)
{
    return qv_next_event(ev, timeout_ms);
}
