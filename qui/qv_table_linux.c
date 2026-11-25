//go:build cgo && !quevu_nogui

/*
 * qv_table - the table of a control tab, with GTK widgets.
 *
 * One row per control, in columns that line up: a light, the name, the
 * text of its state and the buttons of its verbs. The table knows nothing
 * of what any of it means. The host says what color the light is, what
 * the text says and which verbs there are; a button pressed goes back to
 * the host as an event and changes nothing here.
 */
#include <stdlib.h>
#include <string.h>

#include "qv_gtk_linux.h"

/* How often a light that blinks changes, in milliseconds. */
#define BLINK_MS 500
/* The side of the square a light is drawn in, in pixels. */
#define LIGHT 20

typedef struct row {
    uint32_t   id;
    uint32_t   rgb; /* as the host gives it: 0x00RRGGBB */
    uint32_t   attrs;
    qv_verbs   verbs;
    qv_table  *table;
    GtkWidget *light, *name, *text, *buttons, *line;
} row;

struct qv_table {
    GtkWidget *scroller, *grid;
    row      **rows; /* each on its own, so a widget can point at one */
    size_t     nrows, cap;
    int        dark; /* the half of the time a blinking light is off  */
    guint      timer;
};

static int find_row(const qv_table *t, uint32_t id)
{
    size_t i;
    for (i = 0; i < t->nrows; i++)
        if (t->rows[i]->id == id)
            return (int)i;
    return -1;
}

int qv_table_has(const qv_table *t, uint32_t id)
{
    return find_row(t, id) >= 0;
}

static gboolean on_blink(gpointer data)
{
    qv_table *t = data;
    size_t    i;
    t->dark = !t->dark;
    for (i = 0; i < t->nrows; i++)
        if (t->rows[i]->attrs & QUEVU_LIGHT_BLINK)
            G(gtk_widget_queue_draw)(t->rows[i]->light);
    return TRUE;
}

/* Keeps the timer of the lights running only while a light blinks. */
static void watch_blink(qv_table *t)
{
    size_t i;
    int    any = 0;
    for (i = 0; i < t->nrows; i++)
        if (t->rows[i]->attrs & QUEVU_LIGHT_BLINK)
            any = 1;
    if (any && !t->timer) {
        t->timer = G(g_timeout_add)(BLINK_MS, on_blink, t);
    } else if (!any && t->timer) {
        G(g_source_remove)(t->timer);
        t->timer = 0;
        t->dark = 0;
    }
}

/*
 * The light: a disc of the color the host gave. Hollow, or in the dark
 * half of a blink, only its rim.
 */
static gboolean on_light(GtkWidget *widget, cairo_t *cr, gpointer data)
{
    const row *r = data;
    int    w = G(gtk_widget_get_allocated_width)(widget), h = G(gtk_widget_get_allocated_height)(widget);
    int    off = (r->attrs & QUEVU_LIGHT_HOLLOW) || ((r->attrs & QUEVU_LIGHT_BLINK) && r->table->dark);
    double radius = 6.0;

    G(cairo_set_source_rgb)(cr, ((r->rgb >> 16) & 0xff) / 255.0, ((r->rgb >> 8) & 0xff) / 255.0,
                            (r->rgb & 0xff) / 255.0);
    if (off) {
        G(cairo_set_line_width)(cr, 1.5);
        G(cairo_arc)(cr, w / 2.0, h / 2.0, radius - 0.75, 0, 2 * G_PI);
        G(cairo_stroke)(cr);
    } else {
        G(cairo_arc)(cr, w / 2.0, h / 2.0, radius, 0, 2 * G_PI);
        G(cairo_fill)(cr);
    }
    return TRUE;
}

/*
 * A button of a row was pressed: the host hears which verb of which
 * control. Nothing changes here until the host says so.
 */
static void on_verb(GtkWidget *button, gpointer data)
{
    const row *r = data;
    uint32_t   k = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)button, "qv-verb"));
    if (k < r->verbs.n)
        qv_emit(QUEVU_EV_CONTROL_VERB, r->id, r->verbs.v[k].id, 0, 0);
}

static GtkWidget *label(const char *text, int grows)
{
    GtkWidget *l = G(gtk_label_new)(text);
    G(gtk_label_set_xalign)((GtkLabel *)l, 0);
    G(gtk_label_set_ellipsize)((GtkLabel *)l, PANGO_ELLIPSIZE_END);
    if (grows)
        G(gtk_widget_set_hexpand)(l, TRUE);
    return l;
}

/* Puts the widgets of a row on the line of the grid it belongs to. */
static void attach(qv_table *t, row *r, int at)
{
    /* Two lines of the grid per row: the row, and a thin rule under it. */
    G(gtk_grid_attach)((GtkGrid *)t->grid, r->light, 0, at * 2, 1, 1);
    G(gtk_grid_attach)((GtkGrid *)t->grid, r->name, 1, at * 2, 1, 1);
    G(gtk_grid_attach)((GtkGrid *)t->grid, r->text, 2, at * 2, 1, 1);
    G(gtk_grid_attach)((GtkGrid *)t->grid, r->buttons, 3, at * 2, 1, 1);
    G(gtk_grid_attach)((GtkGrid *)t->grid, r->line, 0, at * 2 + 1, 4, 1);
}

/*
 * A new row, at the end. It shows its name and nothing else until the
 * host says what state it is in.
 */
void qv_table_add(qv_table *t, uint32_t id, char *name)
{
    row *r = qv_alloc(sizeof *r);

    if (t->nrows == t->cap) {
        t->cap = t->cap ? t->cap * 2 : 16;
        t->rows = qv_grow(t->rows, t->cap * sizeof *t->rows);
    }
    r->id = id;
    r->attrs = QUEVU_LIGHT_HOLLOW;
    r->table = t;
    r->light = G(gtk_drawing_area_new)();
    G(gtk_widget_set_size_request)(r->light, LIGHT, LIGHT);
    G(gtk_widget_set_valign)(r->light, GTK_ALIGN_CENTER);
    QV_SIGNAL(r->light, "draw", on_light, r);
    r->name = label(name, 0);
    r->text = label("", 1);
    r->buttons = G(gtk_box_new)(GTK_ORIENTATION_HORIZONTAL, 6);
    G(gtk_widget_set_halign)(r->buttons, GTK_ALIGN_START);
    r->line = G(gtk_separator_new)(GTK_ORIENTATION_HORIZONTAL);
    free(name);

    attach(t, r, (int)t->nrows);
    t->rows[t->nrows++] = r;
    G(gtk_widget_show_all)(t->grid);
}

void qv_table_remove(qv_table *t, uint32_t id)
{
    int  i = find_row(t, id);
    row *r;

    if (i < 0)
        return;
    r = t->rows[i];
    /* Both lines of the grid go, and the ones below move up. */
    G(gtk_grid_remove_row)((GtkGrid *)t->grid, i * 2 + 1);
    G(gtk_grid_remove_row)((GtkGrid *)t->grid, i * 2);
    qv_verbs_free(&r->verbs);
    free(r);
    memmove(&t->rows[i], &t->rows[i + 1], (t->nrows - (size_t)i - 1) * sizeof *t->rows);
    t->nrows--;
    watch_blink(t);
}

static void drop_child(GtkWidget *child, gpointer data)
{
    (void)data;
    G(gtk_widget_destroy)(child);
}

/*
 * A control changed. The buttons are made again only when the verbs are
 * not the ones already there: a control that only changes its text keeps
 * its buttons, and whichever of them has the keyboard.
 */
void qv_table_set(qv_table *t, uint32_t id, uint32_t rgb, uint32_t attrs, char *text, qv_verbs *verbs)
{
    int      i = find_row(t, id);
    row     *r;
    uint32_t k;

    if (i < 0) {
        free(text);
        qv_verbs_free(verbs);
        return;
    }
    r = t->rows[i];
    r->rgb = rgb;
    r->attrs = attrs;
    G(gtk_label_set_text)((GtkLabel *)r->text, text);
    free(text);
    G(gtk_widget_queue_draw)(r->light);
    if (qv_verbs_equal(&r->verbs, verbs)) {
        qv_verbs_free(verbs);
        watch_blink(t);
        return;
    }
    G(gtk_container_foreach)((GtkContainer *)r->buttons, drop_child, NULL);
    qv_verbs_free(&r->verbs);
    r->verbs = *verbs;
    verbs->v = NULL;
    verbs->n = 0;
    for (k = 0; k < r->verbs.n; k++) {
        char      *words = qv_u8(r->verbs.v[k].label);
        GtkWidget *b = G(gtk_button_new_with_label)(words);
        free(words);
        G(g_object_set_data)((GObject *)b, "qv-verb", GUINT_TO_POINTER(k));
        G(gtk_widget_set_sensitive)(b, (r->verbs.v[k].attrs & QUEVU_VERB_DISABLED) == 0);
        QV_SIGNAL(b, "clicked", on_verb, r);
        G(gtk_box_pack_start)((GtkBox *)r->buttons, b, FALSE, FALSE, 0);
    }
    G(gtk_widget_show_all)(r->buttons);
    watch_blink(t);
}

/* The widget is gone: so is what the table kept. */
static void on_destroy(GtkWidget *widget, gpointer data)
{
    qv_table *t = data;
    size_t    i;

    (void)widget;
    if (t->timer)
        G(g_source_remove)(t->timer);
    for (i = 0; i < t->nrows; i++) {
        qv_verbs_free(&t->rows[i]->verbs);
        free(t->rows[i]);
    }
    free(t->rows);
    free(t);
}

qv_table *qv_table_new(void)
{
    qv_table *t = qv_alloc(sizeof *t);

    t->grid = G(gtk_grid_new)();
    G(gtk_grid_set_column_spacing)((GtkGrid *)t->grid, 12);
    G(gtk_grid_set_row_spacing)((GtkGrid *)t->grid, 4);
    G(gtk_widget_set_margin_start)(t->grid, 12);
    G(gtk_widget_set_margin_end)(t->grid, 12);
    G(gtk_widget_set_margin_top)(t->grid, 6);
    t->scroller = G(gtk_scrolled_window_new)(NULL, NULL);
    G(gtk_scrolled_window_set_policy)((GtkScrolledWindow *)t->scroller, GTK_POLICY_NEVER, GTK_POLICY_AUTOMATIC);
    G(gtk_container_add)((GtkContainer *)t->scroller, t->grid);
    G(gtk_widget_set_vexpand)(t->scroller, TRUE);
    QV_SIGNAL(t->scroller, "destroy", on_destroy, t);
    return t;
}

GtkWidget *qv_table_widget(qv_table *t)
{
    return t->scroller;
}
