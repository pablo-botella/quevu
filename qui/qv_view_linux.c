//go:build cgo && !quevu_nogui

/*
 * qv_view - the pseudo-console as a GTK widget.
 *
 * It is deliberately small: a grid of fixed-width cells drawn straight
 * from the model. Everything about following, pausing and moving by
 * position lives in the model (qv_console); this file only draws lines
 * and turns mouse, wheel, keys and scroll bars into calls on the model.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "qv_gtk_linux.h"

/* How many lines the view keeps, and how many it asks for at a time. */
#define MAX_LINES 3000
#define PAGE 300

#define PAD 4
/* Width of the time margin, in cells: "HH:MM:SS.mmm" and a space. */
#define TIME_COLS 13

#define BG 0x1e1e1e
#define FG 0xd4d4d4
#define NOTE_FG 0x8a8a8a
#define SEL_BG 0x264f78
#define SEL_FG 0xffffff
/* The line a search landed on. */
#define MARK_BG 0x514810

/* A place in the text: a line, by its stable address, and a column. */
typedef struct pos {
    int64_t line;
    int     col;
} pos;

struct qv_view {
    GtkWidget     *box, *area, *vbar, *hbar;
    GtkAdjustment *vadj, *hadj;
    uint32_t       tab;
    qv_console     model;
    PangoFontDescription *font;
    int            cw, ch;   /* width and height of a cell, in pixels    */
    int            measured;
    int            width, height; /* of the area, as last allocated      */
    int            hpos;     /* first visible column                     */
    int            max_cols; /* the longest line in sight                */
    int            has_anchor;
    pos            anchor;   /* where the mouse went down                */
    pos            caret;    /* where it is                              */
    int            dragging;
    int            show_time;
    int            syncing;  /* the scroll bars are being set from here  */
};

static void emit(const qv_view *v, const qv_req *r)
{
    qv_emit(QUEVU_EV_CONSOLE_REQUEST, v->tab, r->op, r->n, r->seq);
}

static void emit_all(const qv_view *v, const qv_reqs *rs)
{
    int i;
    for (i = 0; i < rs->n; i++)
        emit(v, &rs->r[i]);
}

/* Width of the time margin, in pixels. */
static int gutter(const qv_view *v)
{
    return v->show_time ? TIME_COLS * v->cw : 0;
}

static int cols(const qv_view *v)
{
    int n = (v->width - PAD - gutter(v)) / v->cw;
    return n > 1 ? n : 1;
}

static void sync_scroll(qv_view *v)
{
    const qv_console *m = &v->model;
    size_t i, end = m->top + m->rows + 1;
    int    n = cols(v), most;

    v->max_cols = 0;
    for (i = m->top; i < end && i < m->len; i++)
        if ((int)m->lines[i].len > v->max_cols)
            v->max_cols = (int)m->lines[i].len;
    most = v->max_cols - n + 1;
    if (most < 0)
        most = 0;
    if (v->hpos > most)
        v->hpos = most;
    if (v->hpos < 0)
        v->hpos = 0;

    v->syncing = 1;
    G(gtk_adjustment_configure)(v->vadj, (double)m->top, 0, (double)(m->len > m->rows ? m->len : m->rows), 1,
                                (double)(m->rows > 2 ? m->rows - 1 : 1), (double)m->rows);
    G(gtk_adjustment_configure)(v->hadj, (double)v->hpos, 0, (double)(v->max_cols + 1 > n ? v->max_cols + 1 : n), 1,
                                (double)(n > 2 ? n - 1 : 1), (double)n);
    v->syncing = 0;
}

/*
 * The model changed: scroll bars, pixels and the status strip of the
 * window follow.
 */
static void changed(qv_view *v)
{
    sync_scroll(v);
    G(gtk_widget_queue_draw)(v->area);
    qv_gtk_state_changed(v->tab);
}

/* Measures a cell of the font, once the widget can tell. */
static void measure(qv_view *v)
{
    PangoContext     *ctx;
    PangoFontMetrics *fm;
    int               w, h;

    if (v->measured)
        return;
    ctx = G(gtk_widget_get_pango_context)(v->area);
    if (!ctx)
        return;
    fm = G(pango_context_get_metrics)(ctx, v->font, NULL);
    if (!fm)
        return;
    w = G(pango_font_metrics_get_approximate_digit_width)(fm);
    h = G(pango_font_metrics_get_ascent)(fm) + G(pango_font_metrics_get_descent)(fm);
    G(pango_font_metrics_unref)(fm);
    v->cw = (w + PANGO_SCALE - 1) / PANGO_SCALE;
    v->ch = (h + PANGO_SCALE - 1) / PANGO_SCALE;
    if (v->cw < 1)
        v->cw = 1;
    if (v->ch < 1)
        v->ch = 1;
    v->measured = 1;
}

static void resized(qv_view *v)
{
    int rows;
    measure(v);
    rows = v->height / v->ch;
    qv_console_set_rows(&v->model, rows > 1 ? (size_t)rows : 1);
    changed(v);
}

int qv_view_status(const qv_view *v)
{
    return v->model.status;
}

void qv_view_follow(qv_view *v)
{
    qv_req r = qv_console_follow(&v->model);
    emit(v, &r);
    changed(v);
}

void qv_view_pause(qv_view *v)
{
    qv_req r;
    if (qv_console_pause(&v->model, &r))
        emit(v, &r);
    changed(v);
}

void qv_view_goto(qv_view *v, qv_line *lines, uint32_t n, uint64_t mark, int history)
{
    /*
     * A selection is anchored to this window of lines: it only survives
     * if the window does.
     */
    int64_t origin = v->model.origin;
    size_t  held = v->model.len;
    qv_reqs rs;

    qv_console_goto(&v->model, lines, n, mark, history, &rs);
    if (v->model.origin != origin || v->model.len != held) {
        v->has_anchor = 0;
        v->hpos = 0;
    }
    emit_all(v, &rs);
    changed(v);
}

int qv_view_history_offered(const qv_view *v)
{
    return v->model.history_offered;
}

int qv_view_in_history(const qv_view *v)
{
    return v->model.in_history;
}

void qv_view_request_history(qv_view *v)
{
    qv_req r;
    if (qv_console_request_history(&v->model, &r))
        emit(v, &r);
    changed(v);
}

uint64_t qv_view_find_from(const qv_view *v, int forward)
{
    return qv_console_find_from(&v->model, forward);
}

void qv_view_deliver(qv_view *v, uint32_t op, int full, int history, qv_line *lines, uint32_t n)
{
    if (op == QUEVU_OP_FOLLOW) {
        /* The window starts over: addresses mean something else now. */
        v->has_anchor = 0;
        v->hpos = 0;
    }
    qv_console_deliver(&v->model, op, full, lines, n);
    if (op == QUEVU_OP_BEFORE)
        qv_console_offer_history(&v->model, history);
    changed(v);
}

void qv_view_set_show_time(qv_view *v, int on)
{
    v->show_time = on;
    changed(v);
}

static void scroll_by(qv_view *v, ptrdiff_t delta)
{
    qv_reqs rs;
    qv_console_scroll_by(&v->model, delta, &rs);
    emit_all(v, &rs);
    changed(v);
}

static void scroll_to(qv_view *v, size_t top)
{
    qv_reqs rs;
    qv_console_scroll_to(&v->model, top, &rs);
    emit_all(v, &rs);
    changed(v);
}

static void hscroll_to(qv_view *v, int p)
{
    v->hpos = p > 0 ? p : 0;
    changed(v);
}

/* Division that rounds towards minus infinity, for points off the edge. */
static int floor_div(int a, int b)
{
    int q = a / b;
    if ((a % b != 0) && ((a < 0) != (b < 0)))
        q--;
    return q;
}

/* The place in the text under a point of the area. */
static pos hit(const qv_view *v, int x, int y)
{
    pos p;
    p.line = v->model.origin + (int64_t)v->model.top + floor_div(y, v->ch);
    p.col = v->hpos + floor_div(x - PAD - gutter(v) + v->cw / 2, v->cw);
    if (p.col < 0)
        p.col = 0;
    return p;
}

static int pos_cmp(pos a, pos b)
{
    if (a.line != b.line)
        return a.line < b.line ? -1 : 1;
    return a.col < b.col ? -1 : a.col > b.col;
}

/* The selection with its ends in reading order. */
static int selection(const qv_view *v, pos *from, pos *to)
{
    int c;
    if (!v->has_anchor)
        return 0;
    c = pos_cmp(v->anchor, v->caret);
    if (c == 0)
        return 0;
    *from = c < 0 ? v->anchor : v->caret;
    *to = c < 0 ? v->caret : v->anchor;
    return 1;
}

/* The columns of a line that fall inside the selection. */
static void selected_cols(int has, pos a, pos b, int64_t addr, int len, int *from, int *to)
{
    *from = *to = 0;
    if (!has || addr < a.line || addr > b.line)
        return;
    *from = addr == a.line ? (a.col < len ? a.col : len) : 0;
    *to = addr == b.line ? (b.col < len ? b.col : len) : len;
    if (*to < *from)
        *to = *from;
}

static void select_all(qv_view *v)
{
    if (v->model.len == 0)
        return;
    qv_view_pause(v);
    v->has_anchor = 1;
    v->anchor.line = v->model.origin;
    v->anchor.col = 0;
    v->caret.line = v->model.origin + (int64_t)v->model.len - 1;
    v->caret.col = (int)v->model.lines[v->model.len - 1].len;
    changed(v);
}

/* Puts the selected text on the clipboard, one line per chunk. */
void qv_view_copy(const qv_view *v)
{
    const qv_console *m = &v->model;
    pos       a, b;
    size_t    i, total = 1, o = 0;
    uint16_t *out;
    char     *utf8;

    if (!selection(v, &a, &b))
        return;
    for (i = 0; i < m->len; i++)
        total += m->lines[i].len + 1;
    out = qv_alloc(total * sizeof *out);
    for (i = 0; i < m->len; i++) {
        int64_t addr = m->origin + (int64_t)i;
        int     from, to;
        if (addr < a.line || addr > b.line)
            continue;
        selected_cols(1, a, b, addr, (int)m->lines[i].len, &from, &to);
        memcpy(out + o, m->lines[i].text + from, (size_t)(to - from) * sizeof *out);
        o += (size_t)(to - from);
        if (addr < b.line)
            out[o++] = 10;
    }
    utf8 = qv_u8n(out, o);
    G(gtk_clipboard_set_text)(G(gtk_clipboard_get)(GDK_SELECTION_CLIPBOARD), utf8, -1);
    free(utf8);
    free(out);
}

/* The local time of day of a chunk, as "HH:MM:SS.mmm". */
static int clock_text(int64_t time_ns, char *buf, size_t cap)
{
    time_t    secs = (time_t)(time_ns / 1000000000LL);
    int       ms = (int)((time_ns % 1000000000LL) / 1000000LL);
    struct tm local;

    if (ms < 0) {
        ms += 1000;
        secs--;
    }
    if (!localtime_r(&secs, &local))
        return 0;
    return snprintf(buf, cap, "%02d:%02d:%02d.%03d", local.tm_hour, local.tm_min, local.tm_sec, ms);
}

static void set_rgb(cairo_t *cr, uint32_t rgb)
{
    G(cairo_set_source_rgb)(cr, ((rgb >> 16) & 0xff) / 255.0, ((rgb >> 8) & 0xff) / 255.0, (rgb & 0xff) / 255.0);
}

static void fill(cairo_t *cr, int x, int y, int w, int h, uint32_t rgb)
{
    set_rgb(cr, rgb);
    G(cairo_rectangle)(cr, x, y, w, h);
    G(cairo_fill)(cr);
}

/* Draws text at a cell, over a ground of its own when it has one. */
static void draw_text(const qv_view *v, cairo_t *cr, PangoLayout *layout, int x, int y, const char *utf8, int cells,
                      uint32_t fg, uint32_t bg, uint32_t row_bg)
{
    if (bg != row_bg)
        fill(cr, x, y, cells * v->cw, v->ch, bg);
    G(pango_layout_set_text)(layout, utf8, -1);
    set_rgb(cr, fg);
    G(cairo_move_to)(cr, x, y);
    G(pango_cairo_show_layout)(cr, layout);
}

static void draw_run(const qv_view *v, cairo_t *cr, PangoLayout *layout, int y, const qv_line *line, int from,
                     int to, uint32_t fg, uint32_t bg, uint32_t row_bg)
{
    /* Only what is in sight is handed to pango. */
    int   end = v->hpos + cols(v) + 1;
    char *utf8;

    if (from < v->hpos)
        from = v->hpos;
    if (to > end)
        to = end;
    if (to > (int)line->len)
        to = (int)line->len;
    if (to <= from)
        return;
    utf8 = qv_u8n(line->text + from, (size_t)(to - from));
    draw_text(v, cr, layout, PAD + gutter(v) + (from - v->hpos) * v->cw, y, utf8, to - from, fg, bg, row_bg);
    free(utf8);
}

static int int_cmp(const void *a, const void *b)
{
    int x = *(const int *)a, y = *(const int *)b;
    return x < y ? -1 : x > y;
}

static gboolean on_draw(GtkWidget *widget, cairo_t *cr, gpointer data)
{
    qv_view          *v = data;
    const qv_console *m = &v->model;
    PangoLayout      *layout;
    int     w = v->width, h = v->height, has_sel, row;
    pos     a = {0, 0}, b = {0, 0};
    int    *cuts = NULL;
    size_t  cuts_cap = 0;

    (void)widget;
    measure(v);
    fill(cr, 0, 0, w, h, BG);
    layout = G(pango_cairo_create_layout)(cr);
    G(pango_layout_set_font_description)(layout, v->font);

    has_sel = selection(v, &a, &b);
    for (row = 0; (size_t)row <= m->rows && m->top + (size_t)row < m->len; row++) {
        const qv_line *line = &m->lines[m->top + (size_t)row];
        int      y = row * v->ch;
        int      len = (int)line->len;
        int      noted = line->seq == 0;
        uint32_t row_bg = (!noted && m->has_mark && m->mark == line->seq) ? MARK_BG : BG;
        uint32_t base = noted ? NOTE_FG : FG;
        int      from, to;
        size_t   n = 0, k, need = 4 + 2 * (size_t)line->nspans;
        uint32_t s;

        if (row_bg != BG)
            fill(cr, 0, y, w, v->ch, row_bg);
        if (v->show_time && !noted) {
            /*
             * The time sits in a margin of its own: it is not part of
             * the text, so it is never selected or copied.
             */
            char stamp[32];
            if (clock_text(line->time_ns, stamp, sizeof stamp) > 0)
                draw_text(v, cr, layout, PAD, y, stamp, 0, NOTE_FG, row_bg, row_bg);
        }
        selected_cols(has_sel, a, b, m->origin + (int64_t)m->top + row, len, &from, &to);
        /*
         * The line is cut wherever the color or the selection changes
         * and each piece is drawn in one go. The selection wins over the
         * color of the text.
         */
        if (need > cuts_cap) {
            cuts_cap = need * 2;
            cuts = qv_grow(cuts, cuts_cap * sizeof *cuts);
        }
        cuts[n++] = 0;
        cuts[n++] = len;
        cuts[n++] = from;
        cuts[n++] = to;
        for (s = 0; s < line->nspans; s++) {
            cuts[n++] = (int)line->spans[s].start;
            cuts[n++] = (int)(line->spans[s].start + line->spans[s].len);
        }
        qsort(cuts, n, sizeof *cuts, int_cmp);
        for (k = 0; k + 1 < n; k++) {
            int      p = cuts[k], q = cuts[k + 1];
            uint32_t fg = base;
            if (p == q || p < 0 || q > len)
                continue;
            if (from < to && p >= from && q <= to) {
                draw_run(v, cr, layout, y, line, p, q, SEL_FG, SEL_BG, row_bg);
                continue;
            }
            for (s = 0; s < line->nspans; s++)
                if ((int)line->spans[s].start <= p && p < (int)(line->spans[s].start + line->spans[s].len)) {
                    fg = line->spans[s].rgb;
                    break;
                }
            draw_run(v, cr, layout, y, line, p, q, fg, row_bg, row_bg);
        }
    }
    free(cuts);
    G(g_object_unref)(layout);
    return TRUE;
}

static ptrdiff_t page_rows(const qv_view *v)
{
    return (ptrdiff_t)(v->model.rows > 2 ? v->model.rows : 2) - 1;
}

static void on_size(GtkWidget *widget, GdkRectangle *alloc, gpointer data)
{
    qv_view *v = data;
    (void)widget;
    if (alloc->width == v->width && alloc->height == v->height && v->measured)
        return;
    v->width = alloc->width;
    v->height = alloc->height;
    resized(v);
}

static void on_vadj(GtkAdjustment *adj, gpointer data)
{
    qv_view *v = data;
    double   p;
    if (v->syncing)
        return;
    p = G(gtk_adjustment_get_value)(adj);
    scroll_to(v, p > 0 ? (size_t)(p + 0.5) : 0);
}

static void on_hadj(GtkAdjustment *adj, gpointer data)
{
    qv_view *v = data;
    if (v->syncing)
        return;
    hscroll_to(v, (int)(G(gtk_adjustment_get_value)(adj) + 0.5));
}

static gboolean on_scroll(GtkWidget *widget, GdkEventScroll *ev, gpointer data)
{
    qv_view *v = data;
    double   notches = 0;

    (void)widget;
    /* Positive is "towards older output", as a wheel turned up. */
    switch (ev->direction) {
    case GDK_SCROLL_UP:
        notches = 1;
        break;
    case GDK_SCROLL_DOWN:
        notches = -1;
        break;
    case GDK_SCROLL_SMOOTH:
        notches = -ev->delta_y;
        break;
    case GDK_SCROLL_LEFT:
        hscroll_to(v, v->hpos - 8);
        return TRUE;
    case GDK_SCROLL_RIGHT:
        hscroll_to(v, v->hpos + 8);
        return TRUE;
    default:
        break;
    }
    if (notches == 0)
        return TRUE;
    if (ev->state & GDK_SHIFT_MASK) {
        hscroll_to(v, v->hpos - (int)(notches * 8.0));
    } else {
        double lines = notches * 3.0;
        scroll_by(v, -(ptrdiff_t)(lines < 0 ? lines - 0.5 : lines + 0.5));
    }
    return TRUE;
}

static gboolean on_key(GtkWidget *widget, GdkEventKey *ev, gpointer data)
{
    qv_view *v = data;
    int      ctrl = (ev->state & GDK_CONTROL_MASK) != 0;

    (void)widget;
    switch (ev->keyval) {
    case GDK_KEY_Up:
    case GDK_KEY_KP_Up:
        scroll_by(v, -1);
        return TRUE;
    case GDK_KEY_Down:
    case GDK_KEY_KP_Down:
        scroll_by(v, 1);
        return TRUE;
    case GDK_KEY_Page_Up:
    case GDK_KEY_KP_Page_Up:
        scroll_by(v, -page_rows(v));
        return TRUE;
    case GDK_KEY_Page_Down:
    case GDK_KEY_KP_Page_Down:
        scroll_by(v, page_rows(v));
        return TRUE;
    case GDK_KEY_Home:
    case GDK_KEY_KP_Home:
        scroll_to(v, 0);
        return TRUE;
    case GDK_KEY_End:
    case GDK_KEY_KP_End:
        /* End is "take me back to the live output". */
        qv_view_follow(v);
        return TRUE;
    default:
        break;
    }
    if (ctrl && (ev->keyval == GDK_KEY_c || ev->keyval == GDK_KEY_C)) {
        qv_view_copy(v);
        return TRUE;
    }
    if (ctrl && (ev->keyval == GDK_KEY_a || ev->keyval == GDK_KEY_A)) {
        select_all(v);
        return TRUE;
    }
    return FALSE;
}

static gboolean on_press(GtkWidget *widget, GdkEventButton *ev, gpointer data)
{
    qv_view *v = data;
    if (ev->button != 1 || ev->type != GDK_BUTTON_PRESS)
        return FALSE;
    G(gtk_widget_grab_focus)(widget);
    /* A selection needs the text to hold still. */
    qv_view_pause(v);
    v->anchor = hit(v, (int)ev->x, (int)ev->y);
    v->caret = v->anchor;
    v->has_anchor = 1;
    v->dragging = 1;
    changed(v);
    return TRUE;
}

static gboolean on_motion(GtkWidget *widget, GdkEventMotion *ev, gpointer data)
{
    qv_view *v = data;
    int      x = (int)ev->x, y = (int)ev->y;

    (void)widget;
    if (!v->dragging)
        return FALSE;
    /* Dragging past an edge walks the text along. */
    if (y < 0)
        scroll_by(v, -1);
    else if (y >= v->height)
        scroll_by(v, 1);
    if (y > v->height - 1)
        y = v->height - 1;
    if (y < 0)
        y = 0;
    v->caret = hit(v, x, y);
    changed(v);
    return TRUE;
}

static gboolean on_release(GtkWidget *widget, GdkEventButton *ev, gpointer data)
{
    qv_view *v = data;
    (void)widget;
    if (ev->button == 1)
        v->dragging = 0;
    return FALSE;
}

/* The widget is gone: so is what it showed. */
static void on_destroy(GtkWidget *widget, gpointer data)
{
    qv_view *v = data;
    (void)widget;
    qv_console_free(&v->model);
    G(pango_font_description_free)(v->font);
    G(g_object_unref)(v->vadj);
    G(g_object_unref)(v->hadj);
    free(v);
}

qv_view *qv_view_new(uint32_t tab)
{
    qv_view *v = qv_alloc(sizeof *v);

    v->tab = tab;
    v->cw = 8;
    v->ch = 16;
    v->width = v->height = 1;
    qv_console_init(&v->model, MAX_LINES, PAGE);
    v->font = G(pango_font_description_from_string)("Monospace 10");

    v->vadj = G(gtk_adjustment_new)(0, 0, 1, 1, 1, 1);
    v->hadj = G(gtk_adjustment_new)(0, 0, 1, 1, 1, 1);
    G(g_object_ref_sink)(v->vadj);
    G(g_object_ref_sink)(v->hadj);

    v->area = G(gtk_drawing_area_new)();
    G(gtk_widget_set_can_focus)(v->area, TRUE);
    G(gtk_widget_add_events)(v->area, GDK_BUTTON_PRESS_MASK | GDK_BUTTON_RELEASE_MASK | GDK_BUTTON1_MOTION_MASK |
                                          GDK_SCROLL_MASK | GDK_SMOOTH_SCROLL_MASK | GDK_KEY_PRESS_MASK);
    G(gtk_widget_set_hexpand)(v->area, TRUE);
    G(gtk_widget_set_vexpand)(v->area, TRUE);
    v->vbar = G(gtk_scrollbar_new)(GTK_ORIENTATION_VERTICAL, v->vadj);
    v->hbar = G(gtk_scrollbar_new)(GTK_ORIENTATION_HORIZONTAL, v->hadj);

    /* The text, with a bar at its right and another under it. */
    v->box = G(gtk_grid_new)();
    G(gtk_grid_attach)((GtkGrid *)v->box, v->area, 0, 0, 1, 1);
    G(gtk_grid_attach)((GtkGrid *)v->box, v->vbar, 1, 0, 1, 1);
    G(gtk_grid_attach)((GtkGrid *)v->box, v->hbar, 0, 1, 1, 1);

    QV_SIGNAL(v->area, "draw", on_draw, v);
    QV_SIGNAL(v->area, "size-allocate", on_size, v);
    QV_SIGNAL(v->area, "scroll-event", on_scroll, v);
    QV_SIGNAL(v->area, "key-press-event", on_key, v);
    QV_SIGNAL(v->area, "button-press-event", on_press, v);
    QV_SIGNAL(v->area, "motion-notify-event", on_motion, v);
    QV_SIGNAL(v->area, "button-release-event", on_release, v);
    QV_SIGNAL(v->area, "destroy", on_destroy, v);
    QV_SIGNAL(v->vadj, "value-changed", on_vadj, v);
    QV_SIGNAL(v->hadj, "value-changed", on_hadj, v);
    return v;
}

GtkWidget *qv_view_widget(qv_view *v)
{
    return v->box;
}

GtkWidget *qv_view_focus(qv_view *v)
{
    return v->area;
}
