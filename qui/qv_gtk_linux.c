//go:build cgo && !quevu_nogui

/*
 * qv_gtk - the window, with GTK 3.
 *
 * One window with a row of tabs, and nothing else of its own. Every tab
 * is opened by the host, which gives it its name and its weight, and is a
 * whole page: what it shows and, under it, a bar with its tools and its
 * status. There are two kinds:
 *
 * - a source tab holds a pseudo-console over a log, with the search and
 *   the button that holds the view;
 * - a control tab holds a table of controls, each with a light, a name,
 *   the text of its state and the buttons of its verbs.
 *
 * The menu bar is the host's too. The window has no word of any language
 * and decides nothing: what the user does leaves as an event, and the
 * window goes away only when the host says so.
 *
 * Everything here runs on the thread that called qv_win_run. The host
 * reaches it only through the two lanes of qv_shared: commands make this
 * thread drain the queue from its main loop, and what the user does
 * leaves as events.
 *
 * There is no icon in the notification area here: the desktops of Linux
 * do not agree on one.
 */
#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "qv_gtk_linux.h"

/* Pause and play, as on a tape deck. */
#define GLYPH_PAUSE "\xe2\x9d\x9a\xe2\x9d\x9a"
#define GLYPH_PLAY "\xe2\x96\xb6"
/* Arrows, for walking through the matches of a search. */
#define GLYPH_UP "\xe2\x96\xb2"
#define GLYPH_DOWN "\xe2\x96\xbc"
/* "More": opens the rest of what a search can be given. */
#define GLYPH_MORE "\xe2\x8b\xaf"
/* The three lines that open a menu. */
#define GLYPH_MENU "\xe2\x98\xb0"
/* The mark of a tab with output nobody has looked at. */
#define UNSEEN " \xe2\x80\xa2"

/*
 * What can be asked of a source tab. Its buttons, the entries of its
 * menu and the keys that do the same thing share one number.
 */
enum {
    ID_TOGGLE = 110,
    ID_COPY,
    ID_TIME,
    ID_FIND,
    ID_FIND_OLDER,
    ID_FIND_NEWER,
    ID_HISTORY,
    ID_MORE,
    ID_TABMENU
};

/* ---- reaching GTK ---- */

qv_gtk_table qv_g;

static int loaded; /* 0: not tried; 1: there; -1: not there */

int qv_gtk_load(void)
{
    static const char *const names[] = {"libgtk-3.so.0", "libgtk-3.so"};
    void  *lib = NULL;
    size_t i;

    if (loaded)
        return loaded > 0;
    loaded = -1;
    for (i = 0; i < sizeof names / sizeof names[0] && !lib; i++)
        lib = dlopen(names[i], RTLD_NOW | RTLD_GLOBAL);
    if (!lib)
        return 0;
    /*
     * A name asked of the library is looked for in everything it brings
     * along as well: GDK, GLib, cairo and pango are found through it.
     */
#define X(f)                                   \
    qv_g.f = (__typeof__(qv_g.f))dlsym(lib, #f); \
    if (!qv_g.f)                               \
        return 0;
    QV_GTK_SYMS(X)
#undef X
    loaded = 1;
    return 1;
}

int qv_gtk_available(void)
{
    const char *x = getenv("DISPLAY"), *wayland = getenv("WAYLAND_DISPLAY");
    if (!((x && *x) || (wayland && *wayland)))
        return 0;
    return qv_gtk_load();
}

/* ---- strings ---- */

char *qv_u8n(const uint16_t *w, size_t n)
{
    return qv_wide_to_utf8(w, n, NULL);
}

char *qv_u8(const uint16_t *w)
{
    return qv_u8n(w, w ? qv_wlen(w) : 0);
}

char *qv_sdup(const char *s)
{
    size_t n = s ? strlen(s) : 0;
    char  *out = qv_alloc(n + 1);
    if (n)
        memcpy(out, s, n);
    return out;
}

static const char *cs(const char *s)
{
    return s ? s : "";
}

static int sempty(const char *s)
{
    return !s || !*s;
}

/* Replaces an owned string with another the caller hands over. */
static void sset(char **dst, char *owned)
{
    free(*dst);
    *dst = owned;
}

/* The text of a command, as GTK wants it. */
static char *take_text(qv_cmd *c)
{
    return qv_u8(c->text);
}

static char *sjoin(const char *a, const char *b, const char *c)
{
    size_t la = strlen(a), lb = strlen(b), lc = strlen(c);
    char  *out = qv_alloc(la + lb + lc + 1);
    memcpy(out, a, la);
    memcpy(out + la, b, lb);
    memcpy(out + la + lb, c, lc);
    return out;
}

/* ---- what the window holds ---- */

/*
 * What a source tab holds besides its console: its bar, and where its
 * search stands.
 */
typedef struct source {
    qv_view   *view;
    GtkWidget *strip, *toggle, *menu;
    GtkWidget *find_edit, *find_older, *find_newer, *find_more;
    /* The rest of the search: a range of time, in a row that folds away. */
    int        range_open;
    GtkWidget *range_row, *from_label, *from_pick, *to_label, *to_pick;
    GtkWidget *history;
    int        show_time;    /* each line shows when it entered the log     */
    char      *find_status;  /* how the last step of a search went          */
    uint64_t   find_resume;  /* where the next step starts, if asked again  */
    char      *last_query;   /* and what "again" means                      */
    int        last_newer;
    int        find_offer;   /* a search ran out of recent output           */
    int        find_history; /* the user allowed searches into the history  */
    int64_t    last_since, last_until;
    int        was_following;
} source;

/* What a control tab holds: the table, and the verbs of the tab itself. */
typedef struct controls {
    qv_table  *table;
    GtkWidget *bar, *strip, *buttons;
    qv_verbs   verbs;
} controls;

typedef struct tab {
    uint32_t   id;
    char      *title;
    int32_t    weight;
    GtkWidget *page, *label;
    char      *note;   /* what the host has to say in the status strip    */
    int        unseen; /* output arrived while another tab was in front   */
    int        is_source;
    source     s;
    controls   c;
} tab;

/* An entry of one of the host's menus. */
typedef struct menu_item {
    uint32_t id, parent;
    char    *label;
    uint32_t attrs;
} menu_item;

typedef struct app {
    GtkWidget *window, *bar, *notebook;
    tab       *tabs; /* in the order they are shown */
    size_t     ntabs, tabs_cap;
    uint32_t   front; /* the tab in front, by its identifier; 0: none */
    char      *texts[QV_TEXT_COUNT];
    menu_item *items;
    size_t     nitems, items_cap;
    int        menus_dirty;
    int        syncing;   /* the row of tabs is being set from here      */
    uint32_t   popup_tab; /* the tab whose menu is open                  */
    int        gone;
} app;

/* One per process, like the window. */
static app *the_app;
static int  wake_pending;

static const char *text_of(const app *a, uint32_t id)
{
    return id < QV_TEXT_COUNT ? cs(a->texts[id]) : "";
}

/* The position of a tab in the row, by its identifier; -1 if not there. */
static int tab_index(const app *a, uint32_t id)
{
    size_t i;
    for (i = 0; i < a->ntabs; i++)
        if (a->tabs[i].id == id)
            return (int)i;
    return -1;
}

/* The tab in front, if there is any. */
static int current(const app *a)
{
    return a->front ? tab_index(a, a->front) : -1;
}

/* The source tab at a position, if that is what is there. */
static source *source_at(app *a, int i)
{
    if (i < 0 || (size_t)i >= a->ntabs || !a->tabs[i].is_source)
        return NULL;
    return &a->tabs[i].s;
}

/* The table that holds a control. */
static qv_table *table_of(app *a, uint32_t control)
{
    size_t i;
    for (i = 0; i < a->ntabs; i++)
        if (!a->tabs[i].is_source && qv_table_has(a->tabs[i].c.table, control))
            return a->tabs[i].c.table;
    return NULL;
}

static void show(GtkWidget *w, int visible)
{
    if (visible)
        G(gtk_widget_show)(w);
    else
        G(gtk_widget_hide)(w);
}

/*
 * Writes the title of a tab, with a mark if it holds output nobody has
 * looked at.
 */
static void retitle(const app *a, size_t i)
{
    const tab *t = &a->tabs[i];
    if (t->unseen) {
        char *label = sjoin(cs(t->title), UNSEEN, "");
        G(gtk_label_set_text)((GtkLabel *)t->label, label);
        free(label);
    } else {
        G(gtk_label_set_text)((GtkLabel *)t->label, cs(t->title));
    }
}

/* Makes the row of tabs agree with which tab is in front. */
static void sync_front(app *a)
{
    int cur = current(a);
    show(a->notebook, a->ntabs > 0);
    if (cur < 0)
        return;
    a->syncing = 1;
    G(gtk_notebook_set_current_page)((GtkNotebook *)a->notebook, cur);
    a->syncing = 0;
}

/*
 * Brings the status strip of a tab up to date. Each tab has its own, so
 * it is kept right whether the tab is in front or not.
 */
static void refresh_strip(app *a, int i)
{
    tab        *t;
    source     *s;
    int         status, following, scroll_offer;
    const char *text, *symbol;
    char       *line;

    if (i < 0 || (size_t)i >= a->ntabs)
        return;
    t = &a->tabs[i];
    if (!t->is_source) {
        /* A table of controls says only what the host has to say. */
        G(gtk_label_set_text)((GtkLabel *)t->c.strip, cs(t->note));
        /* With nothing to say and no verbs of its own it has no bar. */
        show(t->c.bar, t->c.verbs.n > 0 || !sempty(t->note));
        return;
    }
    s = &t->s;
    status = qv_view_status(s->view);
    following = status == QV_FOLLOWING;
    if (following && !s->was_following) {
        /*
         * Back on the live output: the search is over, and so is any
         * leave to look into the history. Only on the way back: a search
         * made while following has things to say too.
         */
        sset(&s->find_status, NULL);
        s->find_resume = 0;
        s->find_offer = 0;
        s->find_history = 0;
    }
    s->was_following = following;
    scroll_offer = qv_view_history_offered(s->view);
    show(s->history, scroll_offer || s->find_offer);
    /*
     * The button shows what pressing it does, as on a tape deck. The
     * text says in the host's words what state the view is in.
     */
    switch (status) {
    case QV_FOLLOWING:
        text = text_of(a, QUEVU_TEXT_FOLLOWING), symbol = GLYPH_PAUSE;
        break;
    case QV_PAUSED:
        text = text_of(a, QUEVU_TEXT_PAUSED), symbol = GLYPH_PLAY;
        break;
    case QV_PAUSED_FULL:
        text = text_of(a, QUEVU_TEXT_PAUSED_FULL), symbol = GLYPH_PLAY;
        break;
    default:
        text = text_of(a, QUEVU_TEXT_WAITING), symbol = GLYPH_PAUSE;
        break;
    }
    /* While a search is going on, the strip tells how it went. */
    if (!sempty(s->find_status))
        line = qv_sdup(s->find_status);
    else {
        const char *state = scroll_offer ? text_of(a, QUEVU_TEXT_HISTORY_OFFER) : text;
        if (sempty(t->note))
            line = qv_sdup(state);
        else
            line = sjoin(state, sempty(state) ? "" : "   |   ", t->note);
    }
    G(gtk_label_set_text)((GtkLabel *)s->strip, line);
    free(line);
    G(gtk_button_set_label)((GtkButton *)s->toggle, symbol);
    G(gtk_widget_set_sensitive)(s->toggle, TRUE);
}

void qv_gtk_state_changed(uint32_t tab)
{
    app *a = the_app;
    if (a && !a->gone)
        refresh_strip(a, tab_index(a, tab));
}

/* The tab in front was looked at: its mark goes, its strip is fresh. */
static void show_current(app *a)
{
    int cur = current(a);
    if (cur < 0)
        return;
    if (a->tabs[cur].unseen) {
        a->tabs[cur].unseen = 0;
        retitle(a, (size_t)cur);
    }
    refresh_strip(a, cur);
}

/* ---- what a source tab is asked ---- */

/*
 * The time a box holds, as nanoseconds since the Unix epoch, or zero if
 * it is empty or cannot be read. It is written as the machine tells the
 * time: "2025-03-14 16:05:00", or the day alone.
 */
static int64_t picked(GtkWidget *entry)
{
    const char *text = G(gtk_entry_get_text)((GtkEntry *)entry);
    struct tm   local;
    int         y, mo, d, h = 0, mi = 0, sec = 0;
    time_t      t;

    if (!text || sscanf(text, "%d-%d-%d %d:%d:%d", &y, &mo, &d, &h, &mi, &sec) < 3)
        return 0;
    memset(&local, 0, sizeof local);
    local.tm_year = y - 1900;
    local.tm_mon = mo - 1;
    local.tm_mday = d;
    local.tm_hour = h;
    local.tm_min = mi;
    local.tm_sec = sec;
    local.tm_isdst = -1;
    /*
     * The user thinks in the time of the machine; chunks are stamped in
     * universal time.
     */
    t = mktime(&local);
    if (t == (time_t)-1)
        return 0;
    return (int64_t)t * 1000000000LL;
}

/* The text with its first "%s" replaced by what is being looked for. */
static char *looking_for(const char *format, const char *text)
{
    const char *p = strstr(format, "%s");
    char       *head, *out;
    size_t      n;

    if (!p)
        return qv_sdup(format);
    n = (size_t)(p - format);
    head = qv_alloc(n + 1);
    memcpy(head, format, n);
    out = sjoin(head, text, p + 2);
    free(head);
    return out;
}

/*
 * Asks the host for one step of a search in a console. A search never
 * runs here, and never runs to the end of the log by itself: each step
 * is bounded, and going on is the user's choice.
 */
static void find(app *a, int i, int newer)
{
    source     *s = source_at(a, i);
    uint32_t    id;
    const char *typed;
    char       *query;
    int         blank = 1, same, history;
    int64_t     since = 0, until = 0;
    uint64_t    from;
    size_t      k;

    if (!s)
        return;
    id = a->tabs[i].id;
    typed = G(gtk_entry_get_text)((GtkEntry *)s->find_edit);
    query = qv_sdup(typed);
    for (k = 0; query[k]; k++)
        if (query[k] != ' ' && query[k] != '\t' && query[k] != '\r' && query[k] != '\n')
            blank = 0;
    if (blank) {
        free(query);
        G(gtk_widget_grab_focus)(s->find_edit);
        return;
    }
    /*
     * The range counts only while its row is open: a bound nobody can
     * see must not narrow a search behind the user's back.
     */
    if (s->range_open) {
        since = picked(s->from_pick);
        until = picked(s->to_pick);
        /* The box counts in seconds: the whole second is inside. */
        if (until != 0)
            until += 999999999;
    }
    /*
     * The same question again after a step that ran out of budget goes
     * on from where that step stopped.
     */
    same = s->last_query && strcmp(s->last_query, query) == 0 && s->last_newer == newer && s->last_since == since &&
           s->last_until == until;
    s->last_since = since;
    s->last_until = until;
    if (s->find_resume != 0 && same)
        from = s->find_resume;
    else
        from = qv_view_find_from(s->view, newer);
    sset(&s->last_query, qv_sdup(query));
    s->last_newer = newer;
    s->find_resume = 0;
    sset(&s->find_status, looking_for(text_of(a, QUEVU_TEXT_LOOKING), query));
    s->find_offer = 0;
    /*
     * The history is searched only if the user opened it here, by
     * scrolling into it or by accepting it for a search.
     */
    history = s->find_history || qv_view_in_history(s->view);
    /* The lane keeps the text from here on. */
    qv_set_find(id, query, since, until);
    qv_emit(QUEVU_EV_CONSOLE_FIND, id, (newer ? 1u : 0u) | (history ? 2u : 0u), 0, from);
    refresh_strip(a, i);
}

static void source_command(app *a, int i, int id);

/* A label of the host's as a menu of GTK writes it: "&x" marks a key. */
static char *menu_label(const char *s)
{
    size_t n = strlen(s), o = 0, k;
    char  *out = qv_alloc(n * 2 + 8);

    for (k = 0; k < n; k++) {
        if (s[k] == '&') {
            if (s[k + 1] == '&') {
                out[o++] = '&';
                k++;
            } else {
                out[o++] = '_';
            }
        } else if (s[k] == '_') {
            out[o++] = '_';
            out[o++] = '_';
        } else if (s[k] == '\t') {
            /* What follows names the key that does the same. */
            memcpy(out + o, "    ", 4);
            o += 4;
        } else {
            out[o++] = s[k];
        }
    }
    return out;
}

static void on_popup_item(GtkWidget *item, gpointer data)
{
    app *a = data;
    int  id = (int)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)item, "qv-cmd"));
    /*
     * The window went on living while the menu was open: the tab may
     * have moved in the row, or be gone.
     */
    int now = tab_index(a, a->popup_tab);
    if (now >= 0)
        source_command(a, now, id);
}

static void on_popup_done(GtkWidget *menu, gpointer data)
{
    (void)data;
    G(gtk_widget_destroy)(menu);
}

/*
 * Opens the menu of a source tab over its button and does what the user
 * picks. The words are the host's; an entry it gave no words to is left
 * out.
 */
static void tab_popup(app *a, int i)
{
    static const struct {
        uint32_t text;
        int      id;
        int      group;
    } entries[] = {
        {QUEVU_TEXT_MENU_HOLD, ID_TOGGLE, 0},           {QUEVU_TEXT_MENU_COPY, ID_COPY, 0},
        {QUEVU_TEXT_MENU_FIND, ID_FIND, 1},             {QUEVU_TEXT_MENU_FIND_OLDER, ID_FIND_OLDER, 1},
        {QUEVU_TEXT_MENU_FIND_NEWER, ID_FIND_NEWER, 1}, {QUEVU_TEXT_MENU_TIME, ID_TIME, 2},
    };
    source    *s = source_at(a, i);
    GtkWidget *popup;
    int        any = 0, last_group = -1;
    size_t     k;

    if (!s)
        return;
    popup = G(gtk_menu_new)();
    for (k = 0; k < sizeof entries / sizeof entries[0]; k++) {
        const char *label = text_of(a, entries[k].text);
        char       *words;
        GtkWidget  *item;
        if (sempty(label))
            continue;
        if (any && entries[k].group != last_group)
            G(gtk_menu_shell_append)((GtkMenuShell *)popup, G(gtk_separator_menu_item_new)());
        last_group = entries[k].group;
        words = menu_label(label);
        if (entries[k].id == ID_TIME && s->show_time) {
            item = G(gtk_check_menu_item_new_with_mnemonic)(words);
            G(gtk_check_menu_item_set_active)((GtkCheckMenuItem *)item, TRUE);
        } else {
            item = G(gtk_menu_item_new_with_mnemonic)(words);
        }
        free(words);
        G(g_object_set_data)((GObject *)item, "qv-cmd", GUINT_TO_POINTER((guint)entries[k].id));
        QV_SIGNAL(item, "activate", on_popup_item, a);
        G(gtk_menu_shell_append)((GtkMenuShell *)popup, item);
        any = 1;
    }
    if (!any) {
        G(gtk_widget_destroy)(popup);
        return;
    }
    a->popup_tab = a->tabs[i].id;
    G(gtk_menu_attach_to_widget)((GtkMenu *)popup, s->menu, NULL);
    QV_SIGNAL(popup, "selection-done", on_popup_done, NULL);
    G(gtk_widget_show_all)(popup);
    G(gtk_menu_popup_at_widget)((GtkMenu *)popup, s->menu, GDK_GRAVITY_NORTH_EAST, GDK_GRAVITY_SOUTH_EAST, NULL);
}

/* Whether Shift is down in what is being handled right now. */
static int shift_down(void)
{
    GdkModifierType state = 0;
    return G(gtk_get_current_event_state)(&state) && (state & GDK_SHIFT_MASK) != 0;
}

/*
 * Something asked of the source tab at i: by a button of its bar, an
 * entry of its menu or a key.
 */
static void source_command(app *a, int i, int id)
{
    source    *s = source_at(a, i);
    GtkWidget *view;

    if (!s)
        return;
    view = qv_view_focus(s->view);
    switch (id) {
    case ID_TABMENU:
        tab_popup(a, i);
        break;
    case ID_FIND:
        G(gtk_widget_grab_focus)(s->find_edit);
        G(gtk_editable_select_region)((GtkEditable *)s->find_edit, 0, -1);
        break;
    case ID_MORE:
        /* Folds the rest of the search out, or away. */
        s->range_open = !s->range_open;
        show(s->range_row, s->range_open);
        G(gtk_widget_grab_focus)(s->range_open ? s->from_pick : view);
        break;
    case ID_HISTORY:
        /*
         * The user asks for the history: of the search that just ran
         * out, or of the output on screen.
         */
        if (s->find_offer) {
            s->find_history = 1;
            s->find_offer = 0;
            find(a, i, s->last_newer);
        } else {
            qv_view_request_history(s->view);
        }
        G(gtk_widget_grab_focus)(view);
        break;
    case ID_FIND_OLDER:
    case ID_FIND_NEWER:
        find(a, i, id == ID_FIND_NEWER);
        /*
         * A click leaves the keyboard on the button; it goes back to the
         * console, where the next key is expected.
         */
        if (G(gtk_widget_has_focus)(s->find_older) || G(gtk_widget_has_focus)(s->find_newer))
            G(gtk_widget_grab_focus)(view);
        break;
    case ID_TIME:
        s->show_time = !s->show_time;
        qv_view_set_show_time(s->view, s->show_time);
        break;
    case ID_TOGGLE:
    case ID_COPY:
        if (id == ID_COPY)
            qv_view_copy(s->view);
        else if (qv_view_status(s->view) == QV_FOLLOWING)
            qv_view_pause(s->view);
        else
            qv_view_follow(s->view);
        G(gtk_widget_grab_focus)(view);
        break;
    default:
        break;
    }
}

/* A button of the bar of a source tab was pressed. */
static void on_bar_button(GtkWidget *button, gpointer data)
{
    app     *a = data;
    uint32_t tab_id = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)button, "qv-tab"));
    int      id = (int)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)button, "qv-cmd"));
    int      i = tab_index(a, tab_id);
    if (i >= 0)
        source_command(a, i, id);
}

/* Enter in the search box: a step towards older output, or newer with Shift. */
static void on_find_enter(GtkWidget *entry, gpointer data)
{
    app     *a = data;
    uint32_t tab_id = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)entry, "qv-tab"));
    int      i = tab_index(a, tab_id);
    if (i >= 0)
        find(a, i, shift_down());
}

/* Escape in the search box gives the keyboard back to the console. */
static gboolean on_find_key(GtkWidget *entry, GdkEventKey *ev, gpointer data)
{
    app     *a = data;
    uint32_t tab_id = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)entry, "qv-tab"));
    source  *s = source_at(a, tab_index(a, tab_id));
    if (ev->keyval != GDK_KEY_Escape || !s)
        return FALSE;
    G(gtk_widget_grab_focus)(qv_view_focus(s->view));
    return TRUE;
}

/* A verb of a control tab: the host hears which one, and nothing changes here. */
static void on_tab_verb(GtkWidget *button, gpointer data)
{
    app     *a = data;
    uint32_t tab_id = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)button, "qv-tab"));
    uint32_t k = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)button, "qv-verb"));
    int      i = tab_index(a, tab_id);
    if (i >= 0 && !a->tabs[i].is_source && k < a->tabs[i].c.verbs.n)
        qv_emit(QUEVU_EV_TAB_VERB, tab_id, a->tabs[i].c.verbs.v[k].id, 0, 0);
}

/* ---- tabs ---- */

/* A button of a bar, which remembers the tab it is on and what it asks. */
static GtkWidget *bar_button(app *a, const char *label, uint32_t tab_id, int id)
{
    GtkWidget *b = G(gtk_button_new_with_label)(label);
    G(g_object_set_data)((GObject *)b, "qv-tab", GUINT_TO_POINTER(tab_id));
    G(g_object_set_data)((GObject *)b, "qv-cmd", GUINT_TO_POINTER((guint)id));
    QV_SIGNAL(b, "clicked", on_bar_button, a);
    return b;
}

/* A line of text that takes what room is left and is cut short if it must. */
static GtkWidget *strip_label(void)
{
    GtkWidget *l = G(gtk_label_new)("");
    G(gtk_label_set_xalign)((GtkLabel *)l, 0);
    G(gtk_label_set_ellipsize)((GtkLabel *)l, PANGO_ELLIPSIZE_END);
    G(gtk_widget_set_hexpand)(l, TRUE);
    return l;
}

static GtkWidget *bar_box(void)
{
    GtkWidget *bar = G(gtk_box_new)(GTK_ORIENTATION_HORIZONTAL, 4);
    G(gtk_widget_set_margin_start)(bar, 6);
    G(gtk_widget_set_margin_end)(bar, 4);
    G(gtk_widget_set_margin_top)(bar, 4);
    G(gtk_widget_set_margin_bottom)(bar, 4);
    return bar;
}

static void pack(GtkWidget *box, GtkWidget *child, int grows)
{
    G(gtk_box_pack_start)((GtkBox *)box, child, grows, grows, 0);
}

/*
 * Puts a new tab in the row where its weight says, and in front if it is
 * the first one.
 */
static void insert_tab(app *a, tab t)
{
    int32_t *weights = qv_alloc(a->ntabs * sizeof *weights);
    size_t   i, at;

    for (i = 0; i < a->ntabs; i++)
        weights[i] = a->tabs[i].weight;
    at = qv_tab_insert_at(weights, a->ntabs, t.weight);
    free(weights);

    t.label = G(gtk_label_new)(cs(t.title));
    if (!a->front)
        a->front = t.id;
    if (a->ntabs == a->tabs_cap) {
        a->tabs_cap = a->tabs_cap ? a->tabs_cap * 2 : 8;
        a->tabs = qv_grow(a->tabs, a->tabs_cap * sizeof *a->tabs);
    }
    memmove(a->tabs + at + 1, a->tabs + at, (a->ntabs - at) * sizeof *a->tabs);
    a->tabs[at] = t;
    a->ntabs++;

    /* A page has to be visible before it can come to the front. */
    G(gtk_widget_show)(t.page);
    a->syncing = 1;
    G(gtk_notebook_insert_page)((GtkNotebook *)a->notebook, t.page, t.label, (gint)at);
    a->syncing = 0;
    sync_front(a);
    show_current(a);
}

static void add_source(app *a, uint32_t id, char *title, int32_t weight)
{
    tab        t;
    source    *s = &t.s;
    GtkWidget *bar, *picks[2];
    int        k;

    memset(&t, 0, sizeof t);
    t.id = id;
    t.title = title;
    t.weight = weight;
    t.is_source = 1;
    t.page = G(gtk_box_new)(GTK_ORIENTATION_VERTICAL, 0);

    s->view = qv_view_new(id);
    pack(t.page, qv_view_widget(s->view), 1);

    /*
     * What else a search can be given lives in a row that folds away:
     * for now, a range of time. An empty box is no bound on that side.
     */
    s->range_row = G(gtk_box_new)(GTK_ORIENTATION_HORIZONTAL, 6);
    G(gtk_widget_set_halign)(s->range_row, GTK_ALIGN_END);
    G(gtk_widget_set_margin_end)(s->range_row, 4);
    G(gtk_widget_set_margin_top)(s->range_row, 4);
    s->from_label = G(gtk_label_new)(text_of(a, QUEVU_TEXT_FROM));
    s->from_pick = G(gtk_entry_new)();
    s->to_label = G(gtk_label_new)(text_of(a, QUEVU_TEXT_TO));
    s->to_pick = G(gtk_entry_new)();
    picks[0] = s->from_pick, picks[1] = s->to_pick;
    for (k = 0; k < 2; k++) {
        G(gtk_entry_set_placeholder_text)((GtkEntry *)picks[k], "YYYY-MM-DD HH:MM:SS");
        G(gtk_entry_set_width_chars)((GtkEntry *)picks[k], 20);
    }
    pack(s->range_row, s->from_label, 0);
    pack(s->range_row, s->from_pick, 0);
    pack(s->range_row, s->to_label, 0);
    pack(s->range_row, s->to_pick, 0);
    pack(t.page, s->range_row, 0);

    /*
     * The bar, from its left: the status strip takes what is left; then
     * the way into the history, the search, the button that holds the
     * view and the menu of the tab.
     */
    bar = bar_box();
    s->strip = strip_label();
    /*
     * The one way into the history. It shows up only when there is a
     * history to offer, and nothing goes there until it is pressed.
     */
    s->history = bar_button(a, text_of(a, QUEVU_TEXT_HISTORY), id, ID_HISTORY);
    s->find_edit = G(gtk_entry_new)();
    G(gtk_entry_set_width_chars)((GtkEntry *)s->find_edit, 28);
    G(gtk_entry_set_placeholder_text)((GtkEntry *)s->find_edit, text_of(a, QUEVU_TEXT_FIND_CUE));
    G(g_object_set_data)((GObject *)s->find_edit, "qv-tab", GUINT_TO_POINTER(id));
    QV_SIGNAL(s->find_edit, "activate", on_find_enter, a);
    QV_SIGNAL(s->find_edit, "key-press-event", on_find_key, a);
    /*
     * The same two steps the keys take, for the mouse: up is older, down
     * is newer, as the text itself runs.
     */
    s->find_older = bar_button(a, GLYPH_UP, id, ID_FIND_OLDER);
    s->find_newer = bar_button(a, GLYPH_DOWN, id, ID_FIND_NEWER);
    s->find_more = bar_button(a, GLYPH_MORE, id, ID_MORE);
    /*
     * The button that holds the view shows what pressing it does, as on
     * a tape deck; next to it, the one that opens the menu of the tab.
     */
    s->toggle = bar_button(a, GLYPH_PAUSE, id, ID_TOGGLE);
    G(gtk_widget_set_sensitive)(s->toggle, FALSE);
    s->menu = bar_button(a, GLYPH_MENU, id, ID_TABMENU);
    pack(bar, s->strip, 1);
    pack(bar, s->history, 0);
    pack(bar, s->find_edit, 0);
    pack(bar, s->find_older, 0);
    pack(bar, s->find_newer, 0);
    pack(bar, s->find_more, 0);
    pack(bar, s->toggle, 0);
    pack(bar, s->menu, 0);
    pack(t.page, bar, 0);

    /* Everything on the page shows, but what waits to be asked for. */
    G(gtk_widget_show_all)(t.page);
    G(gtk_widget_hide)(s->range_row);
    G(gtk_widget_hide)(s->history);

    insert_tab(a, t);
    /* The console asks for its first page as soon as it exists. */
    qv_view_follow(s->view);
}

static void add_controls(app *a, uint32_t id, char *title, int32_t weight)
{
    tab t;

    memset(&t, 0, sizeof t);
    t.id = id;
    t.title = title;
    t.weight = weight;
    t.page = G(gtk_box_new)(GTK_ORIENTATION_VERTICAL, 0);
    t.c.table = qv_table_new();
    pack(t.page, qv_table_widget(t.c.table), 1);
    t.c.bar = bar_box();
    t.c.strip = strip_label();
    t.c.buttons = G(gtk_box_new)(GTK_ORIENTATION_HORIZONTAL, 6);
    pack(t.c.bar, t.c.strip, 1);
    pack(t.c.bar, t.c.buttons, 0);
    pack(t.page, t.c.bar, 0);
    G(gtk_widget_show_all)(t.page);
    /* With nothing to say and no verbs of its own it has no bar. */
    G(gtk_widget_hide)(t.c.bar);
    insert_tab(a, t);
}

/* Frees what a tab owns besides its widgets. */
static void free_tab(tab *t)
{
    free(t->title);
    free(t->note);
    free(t->s.find_status);
    free(t->s.last_query);
    qv_verbs_free(&t->c.verbs);
}

static void drop_child(GtkWidget *child, gpointer data)
{
    (void)data;
    G(gtk_widget_destroy)(child);
}

static void tab_verbs(app *a, qv_cmd *c)
{
    int       i = tab_index(a, c->id);
    controls *ct;
    uint32_t  k;

    if (i < 0 || a->tabs[i].is_source)
        return;
    ct = &a->tabs[i].c;
    if (!qv_verbs_equal(&ct->verbs, &c->verbs)) {
        G(gtk_container_foreach)((GtkContainer *)ct->buttons, drop_child, NULL);
        qv_verbs_free(&ct->verbs);
        ct->verbs = c->verbs;
        c->verbs.v = NULL;
        c->verbs.n = 0;
        for (k = 0; k < ct->verbs.n; k++) {
            char      *words = qv_u8(ct->verbs.v[k].label);
            GtkWidget *b = G(gtk_button_new_with_label)(words);
            free(words);
            G(g_object_set_data)((GObject *)b, "qv-tab", GUINT_TO_POINTER(c->id));
            G(g_object_set_data)((GObject *)b, "qv-verb", GUINT_TO_POINTER(k));
            G(gtk_widget_set_sensitive)(b, (ct->verbs.v[k].attrs & QUEVU_VERB_DISABLED) == 0);
            QV_SIGNAL(b, "clicked", on_tab_verb, a);
            pack(ct->buttons, b, 0);
        }
        G(gtk_widget_show_all)(ct->buttons);
    }
    refresh_strip(a, i);
}

static void tab_remove(app *a, uint32_t id)
{
    int i = tab_index(a, id);
    tab gone;

    if (i < 0)
        return;
    gone = a->tabs[i];
    memmove(&a->tabs[i], &a->tabs[i + 1], (a->ntabs - (size_t)i - 1) * sizeof *a->tabs);
    a->ntabs--;
    if (a->front == id) {
        /* The neighbour comes to the front, if there is one. */
        int next = qv_tab_front_after_remove((size_t)i, a->ntabs);
        a->front = next >= 0 ? a->tabs[next].id : 0;
    }
    /* The page takes everything on it along. */
    a->syncing = 1;
    G(gtk_notebook_remove_page)((GtkNotebook *)a->notebook, i);
    a->syncing = 0;
    free_tab(&gone);
    sync_front(a);
    show_current(a);
}

/* The user chose a tab in the row. */
static void on_switch_page(GtkNotebook *notebook, GtkWidget *page, guint index, gpointer data)
{
    app    *a = data;
    source *s;

    (void)notebook;
    (void)page;
    if (a->syncing || a->gone || index >= a->ntabs)
        return;
    a->front = a->tabs[index].id;
    show_current(a);
    if ((s = source_at(a, (int)index)) != NULL)
        G(gtk_widget_grab_focus)(qv_view_focus(s->view));
}

/* ---- menus ---- */

static menu_item *find_item(app *a, uint32_t id)
{
    size_t i;
    for (i = 0; i < a->nitems; i++)
        if (a->items[i].id == id)
            return &a->items[i];
    return NULL;
}

/* An entry of one of the host's menus was chosen. */
static void on_menu(GtkWidget *item, gpointer data)
{
    app     *a = data;
    uint32_t id = (uint32_t)GPOINTER_TO_UINT(G(g_object_get_data)((GObject *)item, "qv-item"));
    menu_item *m;

    if (a->gone)
        return;
    qv_emit(QUEVU_EV_MENU, id, 0, 0, 0);
    /*
     * An entry that carries a mark changes it by itself when chosen
     * here, and a mark is the host's to move: the menus are written
     * again as the host has them.
     */
    if ((m = find_item(a, id)) != NULL && (m->attrs & QUEVU_MENU_CHECKED)) {
        a->menus_dirty = 1;
        qv_gtk_wake();
    }
}

/* Hangs from a menu the entries the host gave for it, and theirs. */
static int build_menu(app *a, GtkWidget *shell, uint32_t parent)
{
    size_t i;
    int    any = 0;

    for (i = 0; i < a->nitems; i++) {
        const menu_item *m = &a->items[i];
        GtkWidget       *item;
        char            *words;

        if (m->parent != parent)
            continue;
        any = 1;
        if (m->attrs & QUEVU_MENU_SEPARATOR) {
            G(gtk_menu_shell_append)((GtkMenuShell *)shell, G(gtk_separator_menu_item_new)());
            continue;
        }
        words = menu_label(cs(m->label));
        if ((m->attrs & QUEVU_MENU_CHECKED) && !(m->attrs & QUEVU_MENU_SUBMENU)) {
            item = G(gtk_check_menu_item_new_with_mnemonic)(words);
            G(gtk_check_menu_item_set_active)((GtkCheckMenuItem *)item, TRUE);
        } else {
            item = G(gtk_menu_item_new_with_mnemonic)(words);
        }
        free(words);
        if (m->attrs & QUEVU_MENU_SUBMENU) {
            GtkWidget *sub = G(gtk_menu_new)();
            build_menu(a, sub, m->id);
            G(gtk_menu_item_set_submenu)((GtkMenuItem *)item, sub);
        } else {
            G(g_object_set_data)((GObject *)item, "qv-item", GUINT_TO_POINTER(m->id));
            QV_SIGNAL(item, "activate", on_menu, a);
        }
        G(gtk_widget_set_sensitive)(item, (m->attrs & QUEVU_MENU_DISABLED) == 0);
        G(gtk_menu_shell_append)((GtkMenuShell *)shell, item);
    }
    return any;
}

/* Writes the menu bar again from the entries the host has given. */
static void rebuild_menus(app *a)
{
    int any;
    a->menus_dirty = 0;
    G(gtk_container_foreach)((GtkContainer *)a->bar, drop_child, NULL);
    any = build_menu(a, a->bar, QUEVU_MENU_BAR);
    /* A bar with nothing on it is not shown at all. */
    if (any)
        G(gtk_widget_show_all)(a->bar);
    else
        G(gtk_widget_hide)(a->bar);
}

static void menu_add(app *a, qv_cmd *c)
{
    menu_item *m;
    if (a->nitems == a->items_cap) {
        a->items_cap = a->items_cap ? a->items_cap * 2 : 16;
        a->items = qv_grow(a->items, a->items_cap * sizeof *a->items);
    }
    m = &a->items[a->nitems++];
    m->id = c->id;
    m->parent = c->tab;
    m->label = take_text(c);
    m->attrs = c->b;
    a->menus_dirty = 1;
}

/* Takes out of the list everything that hangs from an entry. */
static void drop_items_under(app *a, uint32_t parent)
{
    size_t k = 0;
    while (k < a->nitems) {
        if (a->items[k].parent == parent) {
            uint32_t id = a->items[k].id;
            free(a->items[k].label);
            memmove(&a->items[k], &a->items[k + 1], (a->nitems - k - 1) * sizeof *a->items);
            a->nitems--;
            drop_items_under(a, id);
            k = 0;
        } else {
            k++;
        }
    }
}

static void menu_remove(app *a, uint32_t id)
{
    menu_item *m = find_item(a, id);
    if (!m)
        return;
    free(m->label);
    memmove(m, m + 1, (size_t)(&a->items[a->nitems] - (m + 1)) * sizeof *m);
    a->nitems--;
    drop_items_under(a, id);
    a->menus_dirty = 1;
}

/* ---- commands of the host ---- */

/*
 * A text changed: whatever shows it on a tab that already exists is
 * written again.
 */
static void apply_texts(app *a)
{
    size_t i;
    for (i = 0; i < a->ntabs; i++) {
        if (a->tabs[i].is_source) {
            source *s = &a->tabs[i].s;
            G(gtk_entry_set_placeholder_text)((GtkEntry *)s->find_edit, text_of(a, QUEVU_TEXT_FIND_CUE));
            G(gtk_button_set_label)((GtkButton *)s->history, text_of(a, QUEVU_TEXT_HISTORY));
            G(gtk_label_set_text)((GtkLabel *)s->from_label, text_of(a, QUEVU_TEXT_FROM));
            G(gtk_label_set_text)((GtkLabel *)s->to_label, text_of(a, QUEVU_TEXT_TO));
        }
        refresh_strip(a, (int)i);
    }
}

/* Does what a command says. Returns 1 if the window is gone with it. */
static int apply(app *a, qv_cmd *c)
{
    int        i;
    source    *s;
    qv_table  *t;
    menu_item *m;

    switch (c->kind) {
    case QV_CONSOLE_ADD:
        add_source(a, c->id, take_text(c), c->weight);
        break;
    case QV_CONTROLS_ADD:
        add_controls(a, c->id, take_text(c), c->weight);
        break;
    case QV_TAB_TITLE:
        if ((i = tab_index(a, c->id)) >= 0) {
            sset(&a->tabs[i].title, take_text(c));
            retitle(a, (size_t)i);
        }
        break;
    case QV_TAB_SELECT:
        if (tab_index(a, c->id) >= 0) {
            a->front = c->id;
            sync_front(a);
            show_current(a);
        }
        break;
    case QV_TAB_REMOVE:
        tab_remove(a, c->id);
        break;
    case QV_TAB_NOTE:
        if ((i = tab_index(a, c->id)) >= 0) {
            sset(&a->tabs[i].note, take_text(c));
            refresh_strip(a, i);
        }
        break;
    case QV_TAB_VERBS:
        tab_verbs(a, c);
        break;
    case QV_CONTROL_ADD:
        i = tab_index(a, c->tab);
        if (i >= 0 && !a->tabs[i].is_source)
            qv_table_add(a->tabs[i].c.table, c->id, take_text(c));
        break;
    case QV_CONTROL_STATE:
        if ((t = table_of(a, c->id)) != NULL)
            qv_table_set(t, c->id, c->a, c->b, take_text(c), &c->verbs);
        break;
    case QV_CONTROL_REMOVE:
        if ((t = table_of(a, c->id)) != NULL)
            qv_table_remove(t, c->id);
        break;
    case QV_DELIVER: {
        /*
         * New output on a tab that is not in front leaves a mark on its
         * title.
         */
        int fresh = c->a == QUEVU_OP_LIVE && c->nlines > 0;
        i = tab_index(a, c->id);
        if (i < 0)
            break;
        if ((s = source_at(a, i)) != NULL) {
            qv_view_deliver(s->view, c->a, c->full, c->history, c->lines, c->nlines);
            c->nlines = 0;
        }
        if (fresh && current(a) != i && !a->tabs[i].unseen) {
            a->tabs[i].unseen = 1;
            retitle(a, (size_t)i);
        }
        break;
    }
    case QV_CONSOLE_TIME:
        if ((s = source_at(a, tab_index(a, c->id))) != NULL) {
            s->show_time = c->full;
            qv_view_set_show_time(s->view, c->full);
        }
        break;
    case QV_GOTO:
        if ((s = source_at(a, tab_index(a, c->id))) != NULL) {
            qv_view_goto(s->view, c->lines, c->nlines, c->seq, c->history);
            c->nlines = 0;
        }
        break;
    case QV_FIND_STATUS:
        i = tab_index(a, c->id);
        if ((s = source_at(a, i)) != NULL) {
            sset(&s->find_status, take_text(c));
            s->find_resume = c->seq;
            s->find_offer = c->history;
            refresh_strip(a, i);
        }
        break;
    case QV_MENU_ADD:
        menu_add(a, c);
        break;
    case QV_MENU_SET:
        if ((m = find_item(a, c->id)) != NULL) {
            sset(&m->label, take_text(c));
            /*
             * Whether an entry opens a menu is settled when it is added;
             * the rest can change.
             */
            m->attrs = (c->b & ~(uint32_t)QUEVU_MENU_SUBMENU) | (m->attrs & QUEVU_MENU_SUBMENU);
            a->menus_dirty = 1;
        }
        break;
    case QV_MENU_REMOVE:
        menu_remove(a, c->id);
        break;
    case QV_TEXT:
        if (c->id < QV_TEXT_COUNT) {
            sset(&a->texts[c->id], take_text(c));
            apply_texts(a);
        }
        break;
    case QV_TRAY:
        /* No notification area here: see the top of this file. */
        break;
    case QV_SHOW:
        if (c->full) {
            G(gtk_widget_show)(a->window);
            G(gtk_window_present)((GtkWindow *)a->window);
        } else {
            G(gtk_widget_hide)(a->window);
        }
        break;
    case QV_QUIT:
        /* The window goes, and the loop with it. */
        a->gone = 1;
        G(gtk_widget_destroy)(a->window);
        return 1;
    default:
        break;
    }
    return 0;
}

/* Applies everything the host has queued. Returns 1 if the window is gone. */
static int drain(app *a, int starting)
{
    qv_cmd *c = qv_drain();
    int     gone = 0;

    while (c) {
        qv_cmd *next = c->next;
        /*
         * A quit destroys the window: what came after it is dropped. One
         * asked for while no window was open does nothing: it must not
         * close a window that came afterwards.
         */
        if (!gone && !(starting && c->kind == QV_QUIT))
            gone = apply(a, c);
        qv_cmd_free(c);
        c = next;
    }
    if (!gone && a->menus_dirty)
        rebuild_menus(a);
    return gone;
}

static gboolean on_wake(gpointer data)
{
    app *a = the_app;
    (void)data;
    __sync_lock_release(&wake_pending);
    if (a && !a->gone)
        drain(a, 0);
    return FALSE;
}

void qv_gtk_wake(void)
{
    /* One wake at a time is enough: it drains everything there is. */
    if (__sync_bool_compare_and_swap(&wake_pending, 0, 1))
        G(g_idle_add)(on_wake, NULL);
}

/* ---- the window ---- */

/*
 * Closing is the host's decision: it hears the request and, when it sees
 * fit, tells this window to go.
 */
static gboolean on_delete(GtkWidget *window, GdkEvent *ev, gpointer data)
{
    (void)window;
    (void)ev;
    (void)data;
    qv_emit(QUEVU_EV_CLOSE, 0, 0, 0, 0);
    return TRUE;
}

static void on_destroy(GtkWidget *window, gpointer data)
{
    app *a = data;
    (void)window;
    a->gone = 1;
    G(gtk_main_quit)();
}

/*
 * The keys of the search work wherever the keyboard is: in the console,
 * in the search box, on a tab. They are taken here, on the way in, so no
 * widget has to know about them.
 */
static gboolean on_window_key(GtkWidget *window, GdkEventKey *ev, gpointer data)
{
    app *a = data;
    int  shift = (ev->state & GDK_SHIFT_MASK) != 0, ctrl = (ev->state & GDK_CONTROL_MASK) != 0;
    int  id = 0;

    (void)window;
    if (ev->keyval == GDK_KEY_F3)
        id = shift ? ID_FIND_NEWER : ID_FIND_OLDER;
    else if (ctrl && (ev->keyval == GDK_KEY_f || ev->keyval == GDK_KEY_F))
        id = ID_FIND;
    if (!id || current(a) < 0)
        return FALSE;
    source_command(a, current(a), id);
    return TRUE;
}

int qv_win_run(const uint16_t *title)
{
    app       *a;
    GtkWidget *box;
    char      *words;
    size_t     i;

    if (!qv_gtk_load())
        return -1;
    /* No display to open a window on is not a failure: there is no visual part. */
    if (!G(gtk_init_check)(NULL, NULL))
        return -1;

    a = qv_alloc(sizeof *a);
    __sync_lock_release(&wake_pending);
    a->window = G(gtk_window_new)(GTK_WINDOW_TOPLEVEL);
    words = qv_u8(title);
    G(gtk_window_set_title)((GtkWindow *)a->window, words);
    free(words);
    /* A sensible first size. */
    G(gtk_window_set_default_size)((GtkWindow *)a->window, 1040, 660);

    /*
     * The window starts with the row of tabs and nothing in it: no tab,
     * no menu. Both are the host's to add.
     */
    box = G(gtk_box_new)(GTK_ORIENTATION_VERTICAL, 0);
    a->bar = G(gtk_menu_bar_new)();
    a->notebook = G(gtk_notebook_new)();
    G(gtk_notebook_set_scrollable)((GtkNotebook *)a->notebook, TRUE);
    G(gtk_widget_set_margin_start)(a->notebook, 8);
    G(gtk_widget_set_margin_end)(a->notebook, 8);
    G(gtk_widget_set_margin_top)(a->notebook, 8);
    G(gtk_widget_set_margin_bottom)(a->notebook, 8);
    pack(box, a->bar, 0);
    pack(box, a->notebook, 1);
    G(gtk_container_add)((GtkContainer *)a->window, box);
    G(gtk_widget_show)(box);

    QV_SIGNAL(a->window, "delete-event", on_delete, a);
    QV_SIGNAL(a->window, "destroy", on_destroy, a);
    QV_SIGNAL(a->window, "key-press-event", on_window_key, a);
    QV_SIGNAL(a->notebook, "switch-page", on_switch_page, a);

    the_app = a;
    /* What is applied below already has things to tell the host. */
    qv_begin();
    /*
     * Whatever the host queued before the window existed is applied
     * before the window is shown: it opens with its tabs and its menu,
     * not empty and then filling up.
     */
    drain(a, 1);
    G(gtk_widget_show)(a->window);

    /* From here on the host can reach this thread. */
    qv_attach(a->window);
    qv_emit(QUEVU_EV_READY, 0, 0, 0, 0);
    /* Whatever the host queued in the meantime. */
    qv_gtk_wake();

    G(gtk_main)();

    qv_detach();
    the_app = NULL;
    for (i = 0; i < a->ntabs; i++)
        free_tab(&a->tabs[i]);
    free(a->tabs);
    for (i = 0; i < a->nitems; i++)
        free(a->items[i].label);
    free(a->items);
    for (i = 0; i < QV_TEXT_COUNT; i++)
        free(a->texts[i]);
    free(a);
    return 0;
}
