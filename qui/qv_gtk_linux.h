/*
 * qv_gtk_linux.h - what the GTK files of the visual part share: the way
 * GTK is reached, the pseudo-console as a widget and the table of
 * controls as another.
 *
 * GTK is not linked in: it is loaded when the window is first asked for,
 * so a program that carries the window still starts on a machine that
 * has no desktop. Every function of GTK, GDK, GLib, cairo and pango is
 * therefore called through the table below, as G(f)(arguments):
 * the headers give each entry its type, and the loader fills it by name.
 * A function that is not in the list does not compile, which is what
 * keeps a direct call from slipping in.
 *
 * Everything declared here, but for qv_gtk_wake and qv_gtk_available,
 * runs on the thread of the window.
 */
#ifndef QV_GTK_LINUX_H
#define QV_GTK_LINUX_H

/* Casts as plain casts: the checked ones call into GLib directly. */
#define G_DISABLE_CAST_CHECKS
#include <gtk/gtk.h>

#include "qv.h"

/* The functions used: QV_GTK_SYMS(X), one X(function) for each. The list
 * is written by qv_gtk_syms.sh, which reads every G(...) in the sources. */
#include "qv_gtk_syms_linux.h"

typedef struct qv_gtk_table {
#define X(f) __typeof__(&f) f;
    QV_GTK_SYMS(X)
#undef X
} qv_gtk_table;

extern qv_gtk_table qv_g;

#define G(f) (*qv_g.f)

/* Connects a handler to a signal of an object. */
#define QV_SIGNAL(obj, name, fn, data) G(g_signal_connect_data)((obj), (name), G_CALLBACK(fn), (data), NULL, (GConnectFlags)0)

/* Loads GTK if it has not been. Returns 1 if every function was found. */
int qv_gtk_load(void);
/* Whether a window can be opened here: GTK is there and so is a display. */
int qv_gtk_available(void);
/* From any thread: makes the thread of the window drain its queue. */
void qv_gtk_wake(void);

/* UTF-16 as the model keeps it to UTF-8 as GTK wants it; the caller frees. */
char *qv_u8(const uint16_t *w);
char *qv_u8n(const uint16_t *w, size_t n);
/* A copy of a string; null gives "". */
char *qv_sdup(const char *s);

/* The console of tab changed state: its status strip must follow. */
void qv_gtk_state_changed(uint32_t tab);

/* ---- the pseudo-console ---- */

typedef struct qv_view qv_view;

qv_view   *qv_view_new(uint32_t tab);
/* The widget to put on the page: the text and its scroll bars. */
GtkWidget *qv_view_widget(qv_view *v);
/* The part that takes the keyboard. */
GtkWidget *qv_view_focus(qv_view *v);
int        qv_view_status(const qv_view *v);
void       qv_view_follow(qv_view *v);
void       qv_view_pause(qv_view *v);
/* Take what the lines hold, like the model does. */
void       qv_view_deliver(qv_view *v, uint32_t op, int full, int history, qv_line *lines, uint32_t n);
void       qv_view_goto(qv_view *v, qv_line *lines, uint32_t n, uint64_t mark, int history);
int        qv_view_history_offered(const qv_view *v);
int        qv_view_in_history(const qv_view *v);
void       qv_view_request_history(qv_view *v);
uint64_t   qv_view_find_from(const qv_view *v, int forward);
void       qv_view_set_show_time(qv_view *v, int on);
void       qv_view_copy(const qv_view *v);

/* ---- the table of controls ---- */

typedef struct qv_table qv_table;

qv_table  *qv_table_new(void);
GtkWidget *qv_table_widget(qv_table *t);
int        qv_table_has(const qv_table *t, uint32_t id);
/* The name, the text and the verbs given are taken. */
void       qv_table_add(qv_table *t, uint32_t id, char *name);
void       qv_table_set(qv_table *t, uint32_t id, uint32_t rgb, uint32_t attrs, char *text, qv_verbs *verbs);
void       qv_table_remove(qv_table *t, uint32_t id);

#endif /* QV_GTK_LINUX_H */
