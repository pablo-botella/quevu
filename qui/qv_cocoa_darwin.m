//go:build cgo

/*
 * qv_cocoa - the window, with Cocoa.
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
 * The menus are the host's too, and go where macOS puts them: on the bar
 * across the top of the screen. The window has no word of any language
 * and decides nothing: what the user does leaves as an event, and the
 * window goes away only when the host says so.
 *
 * Everything here runs on the main thread, which is the one that called
 * qv_win_run. The host reaches it only through the two lanes of
 * qv_shared: commands make this thread drain the queue from its main
 * queue, and what the user does leaves as events.
 *
 * Memory is kept by hand (no automatic reference counting): what a
 * structure of C holds is retained when stored and released when dropped.
 *
 * There is no icon in the menu bar of the system here yet.
 */
#import <Cocoa/Cocoa.h>

#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "qv.h"

/* Pause and play, as on a tape deck. */
#define GLYPH_PAUSE @"❚❚"
#define GLYPH_PLAY @"▶"
/* Arrows, for walking through the matches of a search. */
#define GLYPH_UP @"▲"
#define GLYPH_DOWN @"▼"
/* "More": opens the rest of what a search can be given. */
#define GLYPH_MORE @"⋯"
/* The three lines that open a menu. */
#define GLYPH_MENU @"☰"
/* The mark of a tab with output nobody has looked at. */
#define UNSEEN @" •"

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

/* How many lines a console keeps, and how many it asks for at a time. */
#define MAX_LINES 3000
#define PAGE 300

#define PAD 4
/* Width of the time margin, in cells: "HH:MM:SS.mmm" and a space. */
#define TIME_COLS 13
/* Thickness of a scroll bar of the console. */
#define BAR 15

#define BG 0x1e1e1e
#define FG 0xd4d4d4
#define NOTE_FG 0x8a8a8a
#define SEL_BG 0x264f78
#define SEL_FG 0xffffff
/* The line a search landed on. */
#define MARK_BG 0x514810

/* How often a light that blinks changes, in seconds. */
#define BLINK 0.5
/* Height of a row of a table of controls. */
#define ROW_H 34

@class QVController;
static QVController *the_app;
static int           wake_pending;

static void state_changed(uint32_t tab);

/* ---- helpers ---- */

static NSColor *color(uint32_t rgb)
{
    return [NSColor colorWithSRGBRed:((rgb >> 16) & 0xff) / 255.0
                               green:((rgb >> 8) & 0xff) / 255.0
                                blue:(rgb & 0xff) / 255.0
                               alpha:1.0];
}

/* UTF-16 as the model keeps it, as a string of Cocoa. Null gives "". */
static NSString *str(const uint16_t *w)
{
    if (!w)
        return @"";
    return [NSString stringWithCharacters:(const unichar *)w length:qv_wlen(w)];
}

static NSString *take_text(qv_cmd *c)
{
    return str(c->text);
}

/* Replaces a string a structure of C holds. */
static void sset(NSString **dst, NSString *s)
{
    [s retain];
    [*dst release];
    *dst = s;
}

static BOOL sempty(NSString *s)
{
    return s == nil || [s length] == 0;
}

/* A label of the host's as a menu of macOS writes it. */
static NSString *menu_label(NSString *s)
{
    NSMutableString *out = [NSMutableString string];
    NSUInteger       n = [s length], k;

    for (k = 0; k < n; k++) {
        unichar c = [s characterAtIndex:k];
        if (c == '&') {
            /* "&x" marks a key, which menus here do not have; "&&" is "&". */
            if (k + 1 < n && [s characterAtIndex:k + 1] == '&') {
                [out appendString:@"&"];
                k++;
            }
        } else if (c == '\t') {
            /* What follows names the key that does the same elsewhere. */
            break;
        } else {
            [out appendFormat:@"%C", c];
        }
    }
    return out;
}

/* A view whose y grows downwards, like the text it lays out. */
@interface QVFlipped : NSView
@end

@implementation QVFlipped
- (BOOL)isFlipped
{
    return YES;
}
@end

/* ==================================================================== */
/* The pseudo-console                                                    */
/* ==================================================================== */

/* A place in the text: a line, by its stable address, and a column. */
typedef struct pos {
    int64_t line;
    int     col;
} pos;

/*
 * It is deliberately small: a grid of fixed-width cells drawn straight
 * from the model. Everything about following, pausing and moving by
 * position lives in the model (qv_console); this only draws lines and
 * turns mouse, wheel, keys and scroll bars into calls on the model.
 */
@interface QVConsole : QVFlipped {
  @public
    uint32_t    tab;
    qv_console  model;
    NSFont     *font;
    CGFloat     cw, ch; /* width and height of a cell                  */
    int         hpos;     /* first visible column                        */
    int         max_cols; /* the longest line in sight                   */
    int         has_anchor;
    pos         anchor;   /* where the mouse went down                   */
    pos         caret;    /* where it is                                 */
    int         show_time;
    NSScroller *vbar, *hbar;
}
- (id)initWithTab:(uint32_t)t;
- (void)changed;
- (void)follow;
- (void)pause;
- (void)copySelection;
@end

static void emit_req(const QVConsole *v, const qv_req *r)
{
    qv_emit(QUEVU_EV_CONSOLE_REQUEST, v->tab, r->op, r->n, r->seq);
}

static void emit_all(const QVConsole *v, const qv_reqs *rs)
{
    int i;
    for (i = 0; i < rs->n; i++)
        emit_req(v, &rs->r[i]);
}

/* Division that rounds towards minus infinity, for points off the edge. */
static int floor_div(int a, int b)
{
    int q = a / b;
    if ((a % b != 0) && ((a < 0) != (b < 0)))
        q--;
    return q;
}

static int pos_cmp(pos a, pos b)
{
    if (a.line != b.line)
        return a.line < b.line ? -1 : 1;
    return a.col < b.col ? -1 : a.col > b.col;
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

static int int_cmp(const void *a, const void *b)
{
    int x = *(const int *)a, y = *(const int *)b;
    return x < y ? -1 : x > y;
}

@implementation QVConsole

- (id)initWithTab:(uint32_t)t
{
    NSSize cell;

    self = [super initWithFrame:NSMakeRect(0, 0, 400, 300)];
    if (!self)
        return nil;
    tab = t;
    qv_console_init(&model, MAX_LINES, PAGE);
    font = [[NSFont userFixedPitchFontOfSize:12] retain];
    if (!font)
        font = [[NSFont systemFontOfSize:12] retain];
    cell = [@"0" sizeWithAttributes:@{NSFontAttributeName : font}];
    cw = ceil(cell.width) > 1 ? ceil(cell.width) : 1;
    ch = ceil(cell.height) > 1 ? ceil(cell.height) : 1;

    /* A bar is upright or lying down by the shape it is made with. */
    vbar = [[NSScroller alloc] initWithFrame:NSMakeRect(0, 0, BAR, 100)];
    hbar = [[NSScroller alloc] initWithFrame:NSMakeRect(0, 0, 100, BAR)];
    for (NSScroller *bar in @[ vbar, hbar ]) {
        [bar setScrollerStyle:NSScrollerStyleLegacy];
        [bar setTarget:self];
        [bar setEnabled:NO];
        [self addSubview:bar];
    }
    [vbar setAction:@selector(vscroll:)];
    [hbar setAction:@selector(hscroll:)];
    return self;
}

- (void)dealloc
{
    qv_console_free(&model);
    [font release];
    [vbar release];
    [hbar release];
    [super dealloc];
}

- (BOOL)acceptsFirstResponder
{
    return YES;
}

/* The part of the view the text is drawn in. */
- (NSSize)textSize
{
    NSSize s = [self bounds].size;
    s.width = s.width - BAR > 1 ? s.width - BAR : 1;
    s.height = s.height - BAR > 1 ? s.height - BAR : 1;
    return s;
}

/* Width of the time margin. */
- (CGFloat)gutter
{
    return show_time ? TIME_COLS * cw : 0;
}

- (int)cols
{
    int n = (int)(([self textSize].width - PAD - [self gutter]) / cw);
    return n > 1 ? n : 1;
}

- (void)syncScroll
{
    const qv_console *m = &model;
    size_t i, end = m->top + m->rows + 1;
    int    n = [self cols], most;

    max_cols = 0;
    for (i = m->top; i < end && i < m->len; i++)
        if ((int)m->lines[i].len > max_cols)
            max_cols = (int)m->lines[i].len;
    most = max_cols - n + 1;
    if (most < 0)
        most = 0;
    if (hpos > most)
        hpos = most;
    if (hpos < 0)
        hpos = 0;

    if (m->len > m->rows) {
        [vbar setEnabled:YES];
        [vbar setKnobProportion:(CGFloat)m->rows / (CGFloat)m->len];
        [vbar setDoubleValue:(double)m->top / (double)(m->len - m->rows)];
    } else {
        [vbar setEnabled:NO];
    }
    if (most > 0) {
        [hbar setEnabled:YES];
        [hbar setKnobProportion:(CGFloat)n / (CGFloat)(max_cols + 1)];
        [hbar setDoubleValue:(double)hpos / (double)most];
    } else {
        [hbar setEnabled:NO];
    }
}

/*
 * The model changed: scroll bars, pixels and the status strip of the
 * window follow.
 */
- (void)changed
{
    [self syncScroll];
    [self setNeedsDisplay:YES];
    state_changed(tab);
}

/* The view has another size: the bars move and the rows are counted again. */
- (void)resizeSubviewsWithOldSize:(NSSize)old
{
    NSSize all = [self bounds].size, text = [self textSize];
    int    rows = (int)(text.height / ch);

    (void)old;
    [vbar setFrame:NSMakeRect(all.width - BAR, 0, BAR, text.height)];
    [hbar setFrame:NSMakeRect(0, all.height - BAR, text.width, BAR)];
    qv_console_set_rows(&model, rows > 1 ? (size_t)rows : 1);
    [self changed];
}

- (void)follow
{
    qv_req r = qv_console_follow(&model);
    emit_req(self, &r);
    [self changed];
}

- (void)pause
{
    qv_req r;
    if (qv_console_pause(&model, &r))
        emit_req(self, &r);
    [self changed];
}

- (void)scrollBy:(ptrdiff_t)delta
{
    qv_reqs rs;
    qv_console_scroll_by(&model, delta, &rs);
    emit_all(self, &rs);
    [self changed];
}

- (void)scrollTo:(size_t)top
{
    qv_reqs rs;
    qv_console_scroll_to(&model, top, &rs);
    emit_all(self, &rs);
    [self changed];
}

- (void)hscrollTo:(int)p
{
    hpos = p > 0 ? p : 0;
    [self changed];
}

- (ptrdiff_t)pageRows
{
    return (ptrdiff_t)(model.rows > 2 ? model.rows : 2) - 1;
}

- (void)vscroll:(NSScroller *)bar
{
    switch ([bar hitPart]) {
    case NSScrollerDecrementPage:
        [self scrollBy:-[self pageRows]];
        break;
    case NSScrollerIncrementPage:
        [self scrollBy:[self pageRows]];
        break;
    case NSScrollerKnob:
    case NSScrollerKnobSlot:
        if (model.len > model.rows)
            [self scrollTo:(size_t)([bar doubleValue] * (double)(model.len - model.rows) + 0.5)];
        break;
    default:
        break;
    }
}

- (void)hscroll:(NSScroller *)bar
{
    int n = [self cols], most = max_cols - n + 1, page = n > 2 ? n - 1 : 1;

    switch ([bar hitPart]) {
    case NSScrollerDecrementPage:
        [self hscrollTo:hpos - page];
        break;
    case NSScrollerIncrementPage:
        [self hscrollTo:hpos + page];
        break;
    case NSScrollerKnob:
    case NSScrollerKnobSlot:
        if (most > 0)
            [self hscrollTo:(int)([bar doubleValue] * most + 0.5)];
        break;
    default:
        break;
    }
}

/* The place in the text under a point of the view. */
- (pos)hit:(NSPoint)p
{
    pos out;
    out.line = model.origin + (int64_t)model.top + floor_div((int)p.y, (int)ch);
    out.col = hpos + floor_div((int)(p.x - PAD - [self gutter] + cw / 2), (int)cw);
    if (out.col < 0)
        out.col = 0;
    return out;
}

/* The selection with its ends in reading order. */
- (int)selectionFrom:(pos *)from to:(pos *)to
{
    int c;
    if (!has_anchor)
        return 0;
    c = pos_cmp(anchor, caret);
    if (c == 0)
        return 0;
    *from = c < 0 ? anchor : caret;
    *to = c < 0 ? caret : anchor;
    return 1;
}

- (void)selectEverything
{
    if (model.len == 0)
        return;
    [self pause];
    has_anchor = 1;
    anchor.line = model.origin;
    anchor.col = 0;
    caret.line = model.origin + (int64_t)model.len - 1;
    caret.col = (int)model.lines[model.len - 1].len;
    [self changed];
}

/* Puts the selected text on the clipboard, one line per chunk. */
- (void)copySelection
{
    const qv_console *m = &model;
    pos              a, b;
    size_t           i;
    NSMutableString *out;
    NSPasteboard    *board;

    if (![self selectionFrom:&a to:&b])
        return;
    out = [NSMutableString string];
    for (i = 0; i < m->len; i++) {
        int64_t addr = m->origin + (int64_t)i;
        int     from, to;
        if (addr < a.line || addr > b.line)
            continue;
        selected_cols(1, a, b, addr, (int)m->lines[i].len, &from, &to);
        if (to > from)
            [out appendString:[NSString stringWithCharacters:(const unichar *)m->lines[i].text + from
                                                      length:(NSUInteger)(to - from)]];
        if (addr < b.line)
            [out appendString:@"\n"];
    }
    board = [NSPasteboard generalPasteboard];
    [board clearContents];
    [board setString:out forType:NSPasteboardTypeString];
}

/* The local time of day of a chunk, as "HH:MM:SS.mmm". */
static NSString *clock_text(int64_t time_ns)
{
    time_t    secs = (time_t)(time_ns / 1000000000LL);
    int       ms = (int)((time_ns % 1000000000LL) / 1000000LL);
    struct tm local;
    char      buf[32];

    if (ms < 0) {
        ms += 1000;
        secs--;
    }
    if (!localtime_r(&secs, &local))
        return nil;
    snprintf(buf, sizeof buf, "%02d:%02d:%02d.%03d", local.tm_hour, local.tm_min, local.tm_sec, ms);
    return [NSString stringWithUTF8String:buf];
}

- (void)drawRun:(const qv_line *)line from:(int)from to:(int)to y:(CGFloat)y fg:(uint32_t)fg bg:(uint32_t)bg
          rowBg:(uint32_t)row_bg
{
    /* Only what is in sight is handed over to be drawn. */
    int       end = hpos + [self cols] + 1;
    CGFloat   x;
    NSString *s;

    if (from < hpos)
        from = hpos;
    if (to > end)
        to = end;
    if (to > (int)line->len)
        to = (int)line->len;
    if (to <= from)
        return;
    x = PAD + [self gutter] + (from - hpos) * cw;
    if (bg != row_bg) {
        [color(bg) setFill];
        NSRectFill(NSMakeRect(x, y, (to - from) * cw, ch));
    }
    s = [[NSString alloc] initWithCharacters:(const unichar *)line->text + from length:(NSUInteger)(to - from)];
    [s drawAtPoint:NSMakePoint(x, y)
        withAttributes:@{NSFontAttributeName : font, NSForegroundColorAttributeName : color(fg)}];
    [s release];
}

- (void)drawRect:(NSRect)dirty
{
    const qv_console *m = &model;
    NSSize  text = [self textSize];
    int     has_sel, row;
    pos     a = {0, 0}, b = {0, 0};
    int    *cuts = NULL;
    size_t  cuts_cap = 0;

    (void)dirty;
    [color(BG) setFill];
    NSRectFill([self bounds]);
    [NSGraphicsContext saveGraphicsState];
    /* Nothing is drawn under the bars. */
    NSRectClip(NSMakeRect(0, 0, text.width, text.height));

    has_sel = [self selectionFrom:&a to:&b];
    for (row = 0; (size_t)row <= m->rows && m->top + (size_t)row < m->len; row++) {
        const qv_line *line = &m->lines[m->top + (size_t)row];
        CGFloat  y = row * ch;
        int      len = (int)line->len;
        int      noted = line->seq == 0;
        uint32_t row_bg = (!noted && m->has_mark && m->mark == line->seq) ? MARK_BG : BG;
        uint32_t base = noted ? NOTE_FG : FG;
        int      from, to;
        size_t   n = 0, k, need = 4 + 2 * (size_t)line->nspans;
        uint32_t s;

        if (row_bg != BG) {
            [color(row_bg) setFill];
            NSRectFill(NSMakeRect(0, y, text.width, ch));
        }
        if (show_time && !noted) {
            /*
             * The time sits in a margin of its own: it is not part of
             * the text, so it is never selected or copied.
             */
            NSString *stamp = clock_text(line->time_ns);
            [stamp drawAtPoint:NSMakePoint(PAD, y)
                withAttributes:@{NSFontAttributeName : font, NSForegroundColorAttributeName : color(NOTE_FG)}];
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
                [self drawRun:line from:p to:q y:y fg:SEL_FG bg:SEL_BG rowBg:row_bg];
                continue;
            }
            for (s = 0; s < line->nspans; s++)
                if ((int)line->spans[s].start <= p && p < (int)(line->spans[s].start + line->spans[s].len)) {
                    fg = line->spans[s].rgb;
                    break;
                }
            [self drawRun:line from:p to:q y:y fg:fg bg:row_bg rowBg:row_bg];
        }
    }
    free(cuts);
    [NSGraphicsContext restoreGraphicsState];
}

- (void)scrollWheel:(NSEvent *)ev
{
    CGFloat dy = [ev scrollingDeltaY], dx = [ev scrollingDeltaX];

    /* A wheel counts in lines; a pad that scrolls smoothly, in points. */
    if ([ev hasPreciseScrollingDeltas]) {
        dy /= ch;
        dx /= cw;
    } else {
        dy *= 3;
        dx *= 8;
    }
    if (([ev modifierFlags] & NSEventModifierFlagShift) != 0 && dx == 0) {
        dx = dy;
        dy = 0;
    }
    /* Up is positive, and means older output. */
    if (dy != 0)
        [self scrollBy:-(ptrdiff_t)(dy < 0 ? dy - 0.5 : dy + 0.5)];
    if (dx != 0)
        [self hscrollTo:hpos - (int)(dx < 0 ? dx - 0.5 : dx + 0.5)];
}

- (void)keyDown:(NSEvent *)ev
{
    NSString *chars = [ev charactersIgnoringModifiers];
    unichar   c = [chars length] > 0 ? [chars characterAtIndex:0] : 0;
    BOOL      cmd = ([ev modifierFlags] & NSEventModifierFlagCommand) != 0;

    switch (c) {
    case NSUpArrowFunctionKey:
        [self scrollBy:-1];
        return;
    case NSDownArrowFunctionKey:
        [self scrollBy:1];
        return;
    case NSPageUpFunctionKey:
        [self scrollBy:-[self pageRows]];
        return;
    case NSPageDownFunctionKey:
        [self scrollBy:[self pageRows]];
        return;
    case NSHomeFunctionKey:
        [self scrollTo:0];
        return;
    case NSEndFunctionKey:
        /* End is "take me back to the live output". */
        [self follow];
        return;
    default:
        break;
    }
    if (cmd && (c == 'c' || c == 'C')) {
        [self copySelection];
        return;
    }
    if (cmd && (c == 'a' || c == 'A')) {
        [self selectEverything];
        return;
    }
    [super keyDown:ev];
}

- (void)mouseDown:(NSEvent *)ev
{
    NSPoint p = [self convertPoint:[ev locationInWindow] fromView:nil];
    [[self window] makeFirstResponder:self];
    /* A selection needs the text to hold still. */
    [self pause];
    anchor = [self hit:p];
    caret = anchor;
    has_anchor = 1;
    [self changed];
}

- (void)mouseDragged:(NSEvent *)ev
{
    NSPoint p = [self convertPoint:[ev locationInWindow] fromView:nil];
    CGFloat h = [self textSize].height;

    /* Dragging past an edge walks the text along. */
    if (p.y < 0)
        [self scrollBy:-1];
    else if (p.y >= h)
        [self scrollBy:1];
    if (p.y > h - 1)
        p.y = h - 1;
    if (p.y < 0)
        p.y = 0;
    caret = [self hit:p];
    [self changed];
}

@end

/* What the window asks of a console. */

static void view_deliver(QVConsole *v, uint32_t op, int full, int history, qv_line *lines, uint32_t n)
{
    if (op == QUEVU_OP_FOLLOW) {
        /* The window starts over: addresses mean something else now. */
        v->has_anchor = 0;
        v->hpos = 0;
    }
    qv_console_deliver(&v->model, op, full, lines, n);
    if (op == QUEVU_OP_BEFORE)
        qv_console_offer_history(&v->model, history);
    [v changed];
}

static void view_goto(QVConsole *v, qv_line *lines, uint32_t n, uint64_t mark, int history)
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
    [v changed];
}

static void view_request_history(QVConsole *v)
{
    qv_req r;
    if (qv_console_request_history(&v->model, &r))
        emit_req(v, &r);
    [v changed];
}

/* ==================================================================== */
/* The table of controls                                                 */
/* ==================================================================== */

typedef struct row {
    uint32_t   id;
    NSString  *name;
    uint32_t   rgb; /* as the host gives it: 0x00RRGGBB */
    uint32_t   attrs;
    NSString  *text;
    qv_verbs   verbs;
    NSButton **buttons; /* one per verb */
} row;

/*
 * One row per control, in columns that line up: a light, the name, the
 * text of its state and the buttons of its verbs. The table knows nothing
 * of what any of it means. The host says what color the light is, what
 * the text says and which verbs there are; a button pressed goes back to
 * the host as an event and changes nothing here.
 */
@interface QVTable : QVFlipped {
  @public
    row     *rows;
    size_t   nrows, cap;
    int      dark; /* the half of the time a blinking light is off  */
    NSTimer *timer;
    CGFloat  name_w, verbs_w;
    NSScrollView *scroller; /* what the table is shown in; not kept  */
}
- (void)arrange;
@end

/* A push button with the host's words on it. */
static NSButton *push_button(NSString *label, BOOL disabled, id target, SEL action)
{
    NSButton *b = [[NSButton alloc] initWithFrame:NSMakeRect(0, 0, 80, 26)];
    [b setTitle:label];
    [b setBezelStyle:NSBezelStyleRounded];
    [b setEnabled:!disabled];
    [b setTarget:target];
    [b setAction:action];
    return b;
}

/* The width a push button wants: its words and some air, within reason. */
static CGFloat button_width(NSButton *b)
{
    CGFloat w = [b fittingSize].width;
    return w > 64 ? w : 64;
}

@implementation QVTable

- (void)dealloc
{
    size_t i;
    [timer invalidate];
    for (i = 0; i < nrows; i++) {
        uint32_t k;
        for (k = 0; k < rows[i].verbs.n; k++)
            [rows[i].buttons[k] release];
        free(rows[i].buttons);
        qv_verbs_free(&rows[i].verbs);
        [rows[i].name release];
        [rows[i].text release];
    }
    free(rows);
    [super dealloc];
}

- (int)find:(uint32_t)id
{
    size_t i;
    for (i = 0; i < nrows; i++)
        if (rows[i].id == id)
            return (int)i;
    return -1;
}

- (void)blink:(NSTimer *)t
{
    (void)t;
    dark = !dark;
    [self setNeedsDisplay:YES];
}

/* Keeps the timer of the lights running only while a light blinks. */
- (void)watchBlink
{
    size_t i;
    int    any = 0;
    for (i = 0; i < nrows; i++)
        if (rows[i].attrs & QUEVU_LIGHT_BLINK)
            any = 1;
    if (any && !timer) {
        /* The table does not keep the timer: it stops it before it goes. */
        timer = [NSTimer scheduledTimerWithTimeInterval:BLINK
                                                 target:self
                                               selector:@selector(blink:)
                                               userInfo:nil
                                                repeats:YES];
    } else if (!any && timer) {
        [timer invalidate];
        timer = nil;
        dark = 0;
    }
}

- (void)dropButtons:(row *)r
{
    uint32_t k;
    for (k = 0; k < r->verbs.n; k++) {
        [r->buttons[k] removeFromSuperview];
        [r->buttons[k] release];
    }
    free(r->buttons);
    r->buttons = NULL;
}

/*
 * A new row, at the end. It shows its name and nothing else until the
 * host says what state it is in.
 */
- (void)add:(uint32_t)id name:(NSString *)name
{
    row *r;
    if (nrows == cap) {
        cap = cap ? cap * 2 : 16;
        rows = qv_grow(rows, cap * sizeof *rows);
    }
    r = &rows[nrows++];
    memset(r, 0, sizeof *r);
    r->id = id;
    r->name = [name retain];
    r->attrs = QUEVU_LIGHT_HOLLOW;
    r->text = [@"" retain];
    [self arrange];
}

- (void)remove:(uint32_t)id
{
    int  i = [self find:id];
    row *r;
    if (i < 0)
        return;
    r = &rows[i];
    [self dropButtons:r];
    qv_verbs_free(&r->verbs);
    [r->name release];
    [r->text release];
    memmove(r, r + 1, (nrows - (size_t)i - 1) * sizeof *r);
    nrows--;
    [self arrange];
}

/*
 * A button of a row was pressed: the host hears which verb of which
 * control. Nothing changes here until the host says so.
 */
- (void)pressed:(NSButton *)button
{
    size_t   i;
    uint32_t k;
    for (i = 0; i < nrows; i++)
        for (k = 0; k < rows[i].verbs.n; k++)
            if (rows[i].buttons[k] == button) {
                qv_emit(QUEVU_EV_CONTROL_VERB, rows[i].id, rows[i].verbs.v[k].id, 0, 0);
                return;
            }
}

/*
 * A control changed. The buttons are made again only when the verbs are
 * not the ones already there: a control that only changes its text keeps
 * its buttons, and whichever of them has the keyboard.
 */
- (void)set:(uint32_t)id rgb:(uint32_t)rgb attrs:(uint32_t)attrs text:(NSString *)text verbs:(qv_verbs *)verbs
{
    int      i = [self find:id];
    row     *r;
    uint32_t k;

    if (i < 0) {
        qv_verbs_free(verbs);
        return;
    }
    r = &rows[i];
    r->rgb = rgb;
    r->attrs = attrs;
    sset(&r->text, text);
    if (qv_verbs_equal(&r->verbs, verbs)) {
        qv_verbs_free(verbs);
        [self watchBlink];
        [self setNeedsDisplay:YES];
        return;
    }
    [self dropButtons:r];
    qv_verbs_free(&r->verbs);
    r->verbs = *verbs;
    verbs->v = NULL;
    verbs->n = 0;
    r->buttons = qv_alloc(r->verbs.n * sizeof *r->buttons);
    for (k = 0; k < r->verbs.n; k++) {
        r->buttons[k] = push_button(str(r->verbs.v[k].label), (r->verbs.v[k].attrs & QUEVU_VERB_DISABLED) != 0, self,
                                    @selector(pressed:));
        [self addSubview:r->buttons[k]];
    }
    [self arrange];
}

/* Works out the columns and puts every button in its place. */
- (void)arrange
{
    NSSize  room = scroller ? [scroller contentSize] : [self bounds].size;
    CGFloat pad = 8, gap = 6, bh = 26, nw = 0, vw = 0, limit, left, content;
    NSDictionary *plain = @{NSFontAttributeName : [NSFont systemFontOfSize:[NSFont systemFontSize]]};
    size_t  i;

    [self watchBlink];
    /*
     * The column of names is as wide as the longest name, within reason;
     * the column of verbs as wide as the longest row of them.
     */
    for (i = 0; i < nrows; i++) {
        CGFloat  w = ceil([rows[i].name sizeWithAttributes:plain].width), total = 0;
        uint32_t k;
        for (k = 0; k < rows[i].verbs.n; k++)
            total += button_width(rows[i].buttons[k]) + (k ? gap : 0);
        if (w > nw)
            nw = w;
        if (total > vw)
            vw = total;
    }
    limit = room.width * 2 / 5;
    if (limit < 60)
        limit = 60;
    name_w = nw < limit ? nw : limit;
    verbs_w = vw;

    /* As tall as its rows, and never shorter than what it is shown in. */
    content = nrows * ROW_H;
    [self setFrameSize:NSMakeSize(room.width, content > room.height ? content : room.height)];

    left = room.width - pad - verbs_w;
    if (left < 0)
        left = 0;
    for (i = 0; i < nrows; i++) {
        CGFloat  y = i * ROW_H + (ROW_H - bh) / 2, x = left;
        uint32_t k;
        for (k = 0; k < rows[i].verbs.n; k++) {
            CGFloat bw = button_width(rows[i].buttons[k]);
            [rows[i].buttons[k] setFrame:NSMakeRect(x, y, bw, bh)];
            x += bw + gap;
        }
    }
    [self setNeedsDisplay:YES];
}

- (void)drawRect:(NSRect)dirty
{
    NSSize  size = [self bounds].size;
    CGFloat pad = 8, d = 12, name_x, text_x, text_end;
    NSMutableParagraphStyle *cut = [[[NSMutableParagraphStyle alloc] init] autorelease];
    NSDictionary *plain;
    size_t  i;

    (void)dirty;
    [[NSColor textBackgroundColor] setFill];
    NSRectFill([self bounds]);
    [cut setLineBreakMode:NSLineBreakByTruncatingTail];
    plain = @{
        NSFontAttributeName : [NSFont systemFontOfSize:[NSFont systemFontSize]],
        NSForegroundColorAttributeName : [NSColor textColor],
        NSParagraphStyleAttributeName : cut
    };

    name_x = pad * 2 + d + pad;
    text_x = name_x + name_w + pad * 2;
    text_end = size.width - pad - verbs_w - pad;
    if (text_end < text_x)
        text_end = text_x;
    for (i = 0; i < nrows; i++) {
        const row   *r = &rows[i];
        CGFloat      top = i * ROW_H, line_h = [NSFont systemFontSize] + 5;
        CGFloat      ty = top + (ROW_H - line_h) / 2;
        NSRect       disc = NSMakeRect(pad * 2, top + (ROW_H - d) / 2, d, d);
        NSBezierPath *light;
        int          off;

        /*
         * The light: a disc of the color the host gave. Hollow, or in
         * the dark half of a blink, only its rim.
         */
        off = (r->attrs & QUEVU_LIGHT_HOLLOW) || ((r->attrs & QUEVU_LIGHT_BLINK) && dark);
        if (off) {
            light = [NSBezierPath bezierPathWithOvalInRect:NSInsetRect(disc, 0.75, 0.75)];
            [light setLineWidth:1.5];
            [color(r->rgb) setStroke];
            [light stroke];
        } else {
            light = [NSBezierPath bezierPathWithOvalInRect:disc];
            [color(r->rgb) setFill];
            [light fill];
        }
        [r->name drawInRect:NSMakeRect(name_x, ty, name_w, line_h) withAttributes:plain];
        [r->text drawInRect:NSMakeRect(text_x, ty, text_end - text_x, line_h) withAttributes:plain];
        /* A thin line under each row: it is a table. */
        [[NSColor gridColor] setFill];
        NSRectFill(NSMakeRect(pad, top + ROW_H - 1, size.width - pad * 2, 1));
    }
}

@end

/* ==================================================================== */
/* The window                                                            */
/* ==================================================================== */

/*
 * What a source tab holds besides its console: its bar, and where its
 * search stands.
 */
typedef struct source {
    QVConsole   *view;
    NSTextField *strip;
    NSButton    *toggle, *menu;
    NSTextField *find_edit;
    NSButton    *find_older, *find_newer, *find_more;
    /* The rest of the search: a range of time, in a row that folds away. */
    int          range_open;
    NSTextField *from_label, *from_pick, *to_label, *to_pick;
    NSButton    *history;
    int          show_time;    /* each line shows when it entered the log     */
    NSString    *find_status;  /* how the last step of a search went          */
    uint64_t     find_resume;  /* where the next step starts, if asked again  */
    NSString    *last_query;   /* and what "again" means                      */
    int          last_newer;
    int          find_offer;   /* a search ran out of recent output           */
    int          find_history; /* the user allowed searches into the history  */
    int64_t      last_since, last_until;
    int          was_following;
} source;

/* What a control tab holds: the table, and the verbs of the tab itself. */
typedef struct controls {
    QVTable      *table;
    NSScrollView *scroller;
    NSTextField  *strip;
    qv_verbs      verbs;
    NSButton    **buttons;
} controls;

@class QVPage;

typedef struct tab {
    uint32_t       id;
    NSString      *title;
    int32_t        weight;
    QVPage        *page;
    NSTabViewItem *item;
    NSString      *note;   /* what the host has to say in the status strip  */
    int            unseen; /* output arrived while another tab was in front */
    int            is_source;
    source         s;
    controls       c;
} tab;

/* An entry of one of the host's menus. */
typedef struct menu_item {
    uint32_t  id, parent;
    NSString *label;
    uint32_t  attrs;
} menu_item;

@interface QVController : NSObject <NSWindowDelegate, NSTabViewDelegate, NSTextFieldDelegate> {
  @public
    NSWindow  *window;
    NSTabView *tabs_ctl;
    tab       *tabs; /* in the order they are shown */
    size_t     ntabs, tabs_cap;
    uint32_t   front; /* the tab in front, by its identifier; 0: none */
    NSString  *texts[QV_TEXT_COUNT];
    menu_item *items;
    size_t     nitems, items_cap;
    int        menus_dirty;
    int        syncing; /* the row of tabs is being set from here */
    int        gone;
    id         key_monitor;
}
- (void)layoutTab:(uint32_t)id;
- (void)refreshStrip:(int)i;
- (void)sourceCommand:(int)i what:(int)id;
- (int)drain:(int)starting;
@end

/* What a tab holds: its content on top and its bar under it. */
@interface QVPage : QVFlipped {
  @public
    uint32_t tab;
}
@end

@implementation QVPage
- (void)resizeSubviewsWithOldSize:(NSSize)old
{
    (void)old;
    [the_app layoutTab:tab];
}
@end

static void state_changed(uint32_t tab_id)
{
    QVController *a = the_app;
    size_t        i;
    if (!a || a->gone)
        return;
    for (i = 0; i < a->ntabs; i++)
        if (a->tabs[i].id == tab_id) {
            [a refreshStrip:(int)i];
            return;
        }
}

/* A line of text of a bar: not to be edited, cut short if it must. */
static NSTextField *label(NSString *text)
{
    NSTextField *l = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 100, 18)];
    [l setStringValue:text];
    [l setBezeled:NO];
    [l setDrawsBackground:NO];
    [l setEditable:NO];
    [l setSelectable:NO];
    [[l cell] setLineBreakMode:NSLineBreakByTruncatingTail];
    return l;
}

/* A box to type in. */
static NSTextField *edit_box(NSString *cue)
{
    NSTextField *e = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 100, 22)];
    [e setPlaceholderString:cue];
    [[e cell] setScrollable:YES];
    [[e cell] setWraps:NO];
    /* Enter says "go", and nothing else does: not leaving the box. */
    [[e cell] setSendsActionOnEndEditing:NO];
    return e;
}

@implementation QVController

- (NSString *)textOf:(uint32_t)id
{
    return id < QV_TEXT_COUNT && texts[id] ? texts[id] : @"";
}

/* The position of a tab in the row, by its identifier; -1 if not there. */
- (int)tabIndex:(uint32_t)id
{
    size_t i;
    for (i = 0; i < ntabs; i++)
        if (tabs[i].id == id)
            return (int)i;
    return -1;
}

/* The tab in front, if there is any. */
- (int)current
{
    return front ? [self tabIndex:front] : -1;
}

/* The source tab at a position, if that is what is there. */
- (source *)sourceAt:(int)i
{
    if (i < 0 || (size_t)i >= ntabs || !tabs[i].is_source)
        return NULL;
    return &tabs[i].s;
}

/* The table that holds a control. */
- (QVTable *)tableOf:(uint32_t)control
{
    size_t i;
    for (i = 0; i < ntabs; i++)
        if (!tabs[i].is_source && [tabs[i].c.table find:control] >= 0)
            return tabs[i].c.table;
    return nil;
}

/*
 * Writes the title of a tab, with a mark if it holds output nobody has
 * looked at.
 */
- (void)retitle:(size_t)i
{
    const tab *t = &tabs[i];
    [t->item setLabel:t->unseen ? [t->title stringByAppendingString:UNSEEN] : t->title];
}

/* Makes the row of tabs agree with which tab is in front. */
- (void)syncFront
{
    int cur = [self current];
    [tabs_ctl setHidden:ntabs == 0];
    if (cur < 0)
        return;
    syncing = 1;
    [tabs_ctl selectTabViewItem:tabs[cur].item];
    syncing = 0;
}

/*
 * Brings the status strip of a tab up to date. Each tab has its own, so
 * it is kept right whether the tab is in front or not.
 */
- (void)refreshStrip:(int)i
{
    tab      *t;
    source   *s;
    int       status, following, scroll_offer, shown;
    NSString *text, *symbol, *line;

    if (i < 0 || (size_t)i >= ntabs)
        return;
    t = &tabs[i];
    if (!t->is_source) {
        /* A table of controls says only what the host has to say. */
        [t->c.strip setStringValue:t->note ? t->note : @""];
        return;
    }
    s = &t->s;
    status = s->view->model.status;
    following = status == QV_FOLLOWING;
    if (following && !s->was_following) {
        /*
         * Back on the live output: the search is over, and so is any
         * leave to look into the history. Only on the way back: a search
         * made while following has things to say too.
         */
        sset(&s->find_status, nil);
        s->find_resume = 0;
        s->find_offer = 0;
        s->find_history = 0;
    }
    s->was_following = following;
    scroll_offer = s->view->model.history_offered;
    shown = scroll_offer || s->find_offer;
    if ([s->history isHidden] == shown) {
        /* The button takes room in the bar, or gives it back. */
        [s->history setHidden:!shown];
        [self layoutTab:t->id];
    }
    /*
     * The button shows what pressing it does, as on a tape deck. The
     * text says in the host's words what state the view is in.
     */
    switch (status) {
    case QV_FOLLOWING:
        text = [self textOf:QUEVU_TEXT_FOLLOWING], symbol = GLYPH_PAUSE;
        break;
    case QV_PAUSED:
        text = [self textOf:QUEVU_TEXT_PAUSED], symbol = GLYPH_PLAY;
        break;
    case QV_PAUSED_FULL:
        text = [self textOf:QUEVU_TEXT_PAUSED_FULL], symbol = GLYPH_PLAY;
        break;
    default:
        text = [self textOf:QUEVU_TEXT_WAITING], symbol = GLYPH_PAUSE;
        break;
    }
    /* While a search is going on, the strip tells how it went. */
    if (!sempty(s->find_status))
        line = s->find_status;
    else {
        NSString *state = scroll_offer ? [self textOf:QUEVU_TEXT_HISTORY_OFFER] : text;
        if (sempty(t->note))
            line = state;
        else if (sempty(state))
            line = t->note;
        else
            line = [NSString stringWithFormat:@"%@   |   %@", state, t->note];
    }
    [s->strip setStringValue:line];
    [s->toggle setTitle:symbol];
    [s->toggle setEnabled:YES];
}

/* The tab in front was looked at: its mark goes, its strip is fresh. */
- (void)showCurrent
{
    int cur = [self current];
    if (cur < 0)
        return;
    if (tabs[cur].unseen) {
        tabs[cur].unseen = 0;
        [self retitle:(size_t)cur];
    }
    [self refreshStrip:cur];
}

/* Lays out what a tab holds: its content on top and its bar under it. */
- (void)layoutTab:(uint32_t)id
{
    int     i = [self tabIndex:id];
    tab    *t;
    NSSize  size;
    CGFloat w, h, pad = 8, bh = 26, fb = 30, bar_h = 36, gap = 2;

    if (i < 0)
        return;
    t = &tabs[i];
    size = [t->page bounds].size;
    w = size.width, h = size.height;

    if (t->is_source) {
        source   *s = &t->s;
        CGFloat   range_h = s->range_open ? 34 : 0;
        CGFloat   top = h - bar_h - range_h > 0 ? h - bar_h - range_h : 0;
        CGFloat   right = w - 4, ry = top + 6, pw = 170, sy, x, fw;
        NSButton *right_side[2] = {s->menu, s->toggle};
        NSButton *finders[3] = {s->find_more, s->find_newer, s->find_older};
        int       k;

        [s->view setFrame:NSMakeRect(0, 0, w, top)];

        /*
         * The folding row sits between the console and the bar, under
         * the search box it belongs to.
         */
        [s->to_pick setFrame:NSMakeRect(right - pw, ry, pw, 22)];
        [s->to_label setFrame:NSMakeRect(right - pw - 34, ry + 3, 30, 18)];
        [s->from_pick setFrame:NSMakeRect(right - pw * 2 - 44, ry, pw, 22)];
        [s->from_label setFrame:NSMakeRect(right - pw * 2 - 92, ry + 3, 44, 18)];

        /*
         * The bar, from its right end: the menu of the tab, the button
         * that holds the view, the search, the way into the history;
         * what is left is the status strip.
         */
        sy = top + range_h + (bar_h - bh) / 2;
        x = right;
        for (k = 0; k < 2; k++) {
            x -= fb;
            [right_side[k] setFrame:NSMakeRect(x, sy, fb, bh)];
            x -= gap;
        }
        x -= pad - gap;
        for (k = 0; k < 3; k++) {
            x -= fb;
            [finders[k] setFrame:NSMakeRect(x, sy, fb, bh)];
            x -= gap;
        }
        fw = 260 < w / 3 ? 260 : w / 3;
        x -= fw;
        [s->find_edit setFrame:NSMakeRect(x, sy + 2, fw, 22)];
        if (![s->history isHidden]) {
            CGFloat hw = button_width(s->history);
            x -= pad + hw;
            [s->history setFrame:NSMakeRect(x, sy, hw, bh)];
        }
        x -= pad;
        [s->strip setFrame:NSMakeRect(6, sy + 4, x - 6 > 0 ? x - 6 : 0, 18)];
    } else {
        controls *c = &t->c;
        CGFloat   top, sy, x, total = 0;
        uint32_t  k;

        /*
         * A control tab with nothing to say and no verbs of its own has
         * no bar: the table takes it all.
         */
        if (c->verbs.n == 0 && sempty(t->note))
            bar_h = 0;
        top = h - bar_h > 0 ? h - bar_h : 0;
        [c->scroller setFrame:NSMakeRect(0, 0, w, top)];
        [c->table arrange];
        sy = top + (bar_h - bh) / 2;
        for (k = 0; k < c->verbs.n; k++)
            total += button_width(c->buttons[k]) + (k ? 6 : 0);
        x = w - 4 - total;
        [c->strip setHidden:bar_h == 0];
        [c->strip setFrame:NSMakeRect(6, sy + 4, x - pad - 6 > 0 ? x - pad - 6 : 0, 18)];
        for (k = 0; k < c->verbs.n; k++) {
            CGFloat bw = button_width(c->buttons[k]);
            [c->buttons[k] setFrame:NSMakeRect(x, sy, bw, bh)];
            x += bw + 6;
        }
    }
}

/* ---- what a source tab is asked ---- */

/*
 * The time a box holds, as nanoseconds since the Unix epoch, or zero if
 * it is empty or cannot be read. It is written as the machine tells the
 * time: "2025-03-14 16:05:00", or the day alone.
 */
static int64_t picked(NSTextField *box)
{
    const char *text = [[box stringValue] UTF8String];
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

/*
 * Asks the host for one step of a search in a console. A search never
 * runs here, and never runs to the end of the log by itself: each step
 * is bounded, and going on is the user's choice.
 */
- (void)find:(int)i newer:(int)newer
{
    source   *s = [self sourceAt:i];
    uint32_t  id;
    NSString *query, *looking;
    NSRange   mark;
    int       same, history;
    int64_t   since = 0, until = 0;
    uint64_t  from;

    if (!s)
        return;
    id = tabs[i].id;
    query = [[[s->find_edit stringValue] copy] autorelease];
    if ([[query stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]] length] == 0) {
        [window makeFirstResponder:s->find_edit];
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
    same = s->last_query && [s->last_query isEqualToString:query] && s->last_newer == newer &&
           s->last_since == since && s->last_until == until;
    s->last_since = since;
    s->last_until = until;
    if (s->find_resume != 0 && same)
        from = s->find_resume;
    else
        from = qv_console_find_from(&s->view->model, newer);
    sset(&s->last_query, query);
    s->last_newer = newer;
    s->find_resume = 0;
    /* The text with its first "%s" replaced by what is being looked for. */
    looking = [self textOf:QUEVU_TEXT_LOOKING];
    mark = [looking rangeOfString:@"%s"];
    if (mark.location != NSNotFound)
        looking = [looking stringByReplacingCharactersInRange:mark withString:query];
    sset(&s->find_status, looking);
    s->find_offer = 0;
    /*
     * The history is searched only if the user opened it here, by
     * scrolling into it or by accepting it for a search.
     */
    history = s->find_history || s->view->model.in_history;
    /* The lane keeps the text from here on. */
    qv_set_find(id, strdup([query UTF8String]), since, until);
    qv_emit(QUEVU_EV_CONSOLE_FIND, id, (newer ? 1u : 0u) | (history ? 2u : 0u), 0, from);
    [self refreshStrip:i];
}

/* An entry of the menu of a source tab was chosen. */
- (void)popupChosen:(NSMenuItem *)item
{
    /* The tab the menu belongs to travels with each of its entries. */
    int now = [self tabIndex:(uint32_t)[[item representedObject] unsignedIntValue]];
    if (now >= 0)
        [self sourceCommand:now what:(int)[item tag]];
}

/*
 * Opens the menu of a source tab over its button and does what the user
 * picks. The words are the host's; an entry it gave no words to is left
 * out.
 */
- (void)tabPopup:(int)i
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
    source *s = [self sourceAt:i];
    NSMenu *popup;
    int     any = 0, last_group = -1;
    size_t  k;

    if (!s)
        return;
    popup = [[[NSMenu alloc] initWithTitle:@""] autorelease];
    [popup setAutoenablesItems:NO];
    for (k = 0; k < sizeof entries / sizeof entries[0]; k++) {
        NSString   *words = [self textOf:entries[k].text];
        NSMenuItem *item;
        if (sempty(words))
            continue;
        if (any && entries[k].group != last_group)
            [popup addItem:[NSMenuItem separatorItem]];
        last_group = entries[k].group;
        item = [popup addItemWithTitle:menu_label(words) action:@selector(popupChosen:) keyEquivalent:@""];
        [item setTarget:self];
        [item setTag:entries[k].id];
        [item setRepresentedObject:[NSNumber numberWithUnsignedInt:tabs[i].id]];
        if (entries[k].id == ID_TIME && s->show_time)
            [item setState:NSControlStateValueOn];
        any = 1;
    }
    if (any)
        [popup popUpMenuPositioningItem:nil atLocation:NSMakePoint(0, 0) inView:s->menu];
}

/*
 * Something asked of the source tab at i: by a button of its bar, an
 * entry of its menu or a key.
 */
- (void)sourceCommand:(int)i what:(int)id
{
    source *s = [self sourceAt:i];

    if (!s)
        return;
    switch (id) {
    case ID_TABMENU:
        [self tabPopup:i];
        break;
    case ID_FIND:
        [window makeFirstResponder:s->find_edit];
        [s->find_edit selectText:nil];
        break;
    case ID_MORE: {
        /* Folds the rest of the search out, or away. */
        BOOL hidden;
        s->range_open = !s->range_open;
        hidden = !s->range_open;
        [s->from_label setHidden:hidden];
        [s->from_pick setHidden:hidden];
        [s->to_label setHidden:hidden];
        [s->to_pick setHidden:hidden];
        [self layoutTab:tabs[i].id];
        [window makeFirstResponder:s->range_open ? (NSView *)s->from_pick : (NSView *)s->view];
        break;
    }
    case ID_HISTORY:
        /*
         * The user asks for the history: of the search that just ran
         * out, or of the output on screen.
         */
        if (s->find_offer) {
            s->find_history = 1;
            s->find_offer = 0;
            [self find:i newer:s->last_newer];
        } else {
            view_request_history(s->view);
        }
        [window makeFirstResponder:s->view];
        break;
    case ID_FIND_OLDER:
    case ID_FIND_NEWER:
        [self find:i newer:id == ID_FIND_NEWER];
        break;
    case ID_TIME:
        s->show_time = !s->show_time;
        s->view->show_time = s->show_time;
        [s->view changed];
        break;
    case ID_TOGGLE:
    case ID_COPY:
        if (id == ID_COPY)
            [s->view copySelection];
        else if (s->view->model.status == QV_FOLLOWING)
            [s->view pause];
        else
            [s->view follow];
        [window makeFirstResponder:s->view];
        break;
    default:
        break;
    }
}

/* A button of the bar of a source tab was pressed. */
- (void)barButton:(NSButton *)button
{
    size_t i;
    /* Its tag says what it asks; which tab it is on, its page tells. */
    for (i = 0; i < ntabs; i++)
        if ([button superview] == tabs[i].page) {
            [self sourceCommand:(int)i what:(int)[button tag]];
            return;
        }
}

/* Enter in the search box: a step towards older output, or newer with Shift. */
- (void)findEnter:(NSTextField *)box
{
    size_t i;
    for (i = 0; i < ntabs; i++)
        if (tabs[i].is_source && tabs[i].s.find_edit == box) {
            [self find:(int)i newer:([NSEvent modifierFlags] & NSEventModifierFlagShift) != 0];
            return;
        }
}

/* Escape in the search box gives the keyboard back to the console. */
- (BOOL)control:(NSControl *)control textView:(NSTextView *)view doCommandBySelector:(SEL)command
{
    size_t i;
    (void)view;
    if (command != @selector(cancelOperation:))
        return NO;
    for (i = 0; i < ntabs; i++)
        if (tabs[i].is_source && tabs[i].s.find_edit == control) {
            [window makeFirstResponder:tabs[i].s.view];
            return YES;
        }
    return NO;
}

/* A verb of a control tab: the host hears which one, and nothing changes here. */
- (void)tabVerb:(NSButton *)button
{
    size_t   i;
    uint32_t k;
    for (i = 0; i < ntabs; i++)
        if (!tabs[i].is_source)
            for (k = 0; k < tabs[i].c.verbs.n; k++)
                if (tabs[i].c.buttons[k] == button) {
                    qv_emit(QUEVU_EV_TAB_VERB, tabs[i].id, tabs[i].c.verbs.v[k].id, 0, 0);
                    return;
                }
}

/* ---- tabs ---- */

/* A button of a bar that shows a symbol instead of words. */
- (NSButton *)glyph:(NSString *)symbol what:(int)id
{
    NSButton *b = [[NSButton alloc] initWithFrame:NSMakeRect(0, 0, 30, 26)];
    [b setTitle:symbol];
    [b setBezelStyle:NSBezelStyleSmallSquare];
    [b setTag:id];
    [b setTarget:self];
    [b setAction:@selector(barButton:)];
    return b;
}

/*
 * Puts a new tab in the row where its weight says, and in front if it is
 * the first one.
 */
- (void)insertTab:(tab)t
{
    int32_t *weights = qv_alloc(ntabs * sizeof *weights);
    size_t   i, at;

    for (i = 0; i < ntabs; i++)
        weights[i] = tabs[i].weight;
    at = qv_tab_insert_at(weights, ntabs, t.weight);
    free(weights);

    t.item = [[NSTabViewItem alloc] initWithIdentifier:nil];
    [t.item setLabel:t.title];
    [t.item setView:t.page];
    if (!front)
        front = t.id;
    if (ntabs == tabs_cap) {
        tabs_cap = tabs_cap ? tabs_cap * 2 : 8;
        tabs = qv_grow(tabs, tabs_cap * sizeof *tabs);
    }
    memmove(tabs + at + 1, tabs + at, (ntabs - at) * sizeof *tabs);
    tabs[at] = t;
    ntabs++;

    syncing = 1;
    [tabs_ctl insertTabViewItem:t.item atIndex:(NSInteger)at];
    syncing = 0;
    [self syncFront];
    [self layoutTab:t.id];
    [self showCurrent];
}

- (void)addSource:(uint32_t)id title:(NSString *)title weight:(int32_t)weight
{
    tab     t;
    source *s = &t.s;

    memset(&t, 0, sizeof t);
    t.id = id;
    t.title = [title retain];
    t.weight = weight;
    t.is_source = 1;
    t.page = [[QVPage alloc] initWithFrame:NSMakeRect(0, 0, 600, 400)];
    t.page->tab = id;

    s->view = [[QVConsole alloc] initWithTab:id];
    s->strip = label(@"");
    /*
     * The one way into the history. It shows up only when there is a
     * history to offer, and nothing goes there until it is pressed.
     */
    s->history = push_button([self textOf:QUEVU_TEXT_HISTORY], NO, self, @selector(barButton:));
    [s->history setTag:ID_HISTORY];
    [s->history setHidden:YES];
    s->find_edit = edit_box([self textOf:QUEVU_TEXT_FIND_CUE]);
    [s->find_edit setTarget:self];
    [s->find_edit setAction:@selector(findEnter:)];
    [s->find_edit setDelegate:self];
    /*
     * The same two steps the keys take, for the mouse: up is older, down
     * is newer, as the text itself runs.
     */
    s->find_older = [self glyph:GLYPH_UP what:ID_FIND_OLDER];
    s->find_newer = [self glyph:GLYPH_DOWN what:ID_FIND_NEWER];
    /*
     * What else a search can be given lives in a row that folds away:
     * for now, a range of time. An empty box is no bound on that side.
     */
    s->find_more = [self glyph:GLYPH_MORE what:ID_MORE];
    s->from_label = label([self textOf:QUEVU_TEXT_FROM]);
    s->from_pick = edit_box(@"YYYY-MM-DD HH:MM:SS");
    s->to_label = label([self textOf:QUEVU_TEXT_TO]);
    s->to_pick = edit_box(@"YYYY-MM-DD HH:MM:SS");
    for (NSView *folded in @[ s->from_label, s->from_pick, s->to_label, s->to_pick ])
        [folded setHidden:YES];
    /*
     * The button that holds the view shows what pressing it does, as on
     * a tape deck; next to it, the one that opens the menu of the tab.
     */
    s->toggle = [self glyph:GLYPH_PAUSE what:ID_TOGGLE];
    [s->toggle setEnabled:NO];
    s->menu = [self glyph:GLYPH_MENU what:ID_TABMENU];

    for (NSView *child in @[
             s->view, s->strip, s->history, s->find_edit, s->find_older, s->find_newer, s->find_more, s->from_label,
             s->from_pick, s->to_label, s->to_pick, s->toggle, s->menu
         ])
        [t.page addSubview:child];

    [self insertTab:t];
    /* The console asks for its first page as soon as it exists. */
    [s->view follow];
}

- (void)addControls:(uint32_t)id title:(NSString *)title weight:(int32_t)weight
{
    tab t;

    memset(&t, 0, sizeof t);
    t.id = id;
    t.title = [title retain];
    t.weight = weight;
    t.page = [[QVPage alloc] initWithFrame:NSMakeRect(0, 0, 600, 400)];
    t.page->tab = id;
    t.c.table = [[QVTable alloc] initWithFrame:NSMakeRect(0, 0, 600, 300)];
    t.c.scroller = [[NSScrollView alloc] initWithFrame:NSMakeRect(0, 0, 600, 300)];
    [t.c.scroller setHasVerticalScroller:YES];
    [t.c.scroller setAutohidesScrollers:YES];
    [t.c.scroller setDocumentView:t.c.table];
    t.c.table->scroller = t.c.scroller;
    t.c.strip = label(@"");
    [t.page addSubview:t.c.scroller];
    [t.page addSubview:t.c.strip];
    [self insertTab:t];
}

/* Lets go of what a tab holds. */
- (void)freeTab:(tab *)t
{
    uint32_t k;
    if (t->is_source) {
        source *s = &t->s;
        for (NSObject *o in @[
                 s->view, s->strip, s->history, s->find_edit, s->find_older, s->find_newer, s->find_more,
                 s->from_label, s->from_pick, s->to_label, s->to_pick, s->toggle, s->menu
             ])
            [o release];
        [s->find_status release];
        [s->last_query release];
    } else {
        for (k = 0; k < t->c.verbs.n; k++)
            [t->c.buttons[k] release];
        free(t->c.buttons);
        qv_verbs_free(&t->c.verbs);
        /* Nothing asks the table about what shows it once that is gone. */
        t->c.table->scroller = nil;
        [t->c.scroller release];
        [t->c.table release];
        [t->c.strip release];
    }
    [t->page release];
    [t->item release];
    [t->title release];
    [t->note release];
}

- (void)tabVerbs:(qv_cmd *)c
{
    int       i = [self tabIndex:c->id];
    controls *ct;
    uint32_t  k;

    if (i < 0 || tabs[i].is_source)
        return;
    ct = &tabs[i].c;
    if (!qv_verbs_equal(&ct->verbs, &c->verbs)) {
        for (k = 0; k < ct->verbs.n; k++) {
            [ct->buttons[k] removeFromSuperview];
            [ct->buttons[k] release];
        }
        free(ct->buttons);
        qv_verbs_free(&ct->verbs);
        ct->verbs = c->verbs;
        c->verbs.v = NULL;
        c->verbs.n = 0;
        ct->buttons = qv_alloc(ct->verbs.n * sizeof *ct->buttons);
        for (k = 0; k < ct->verbs.n; k++) {
            ct->buttons[k] = push_button(str(ct->verbs.v[k].label), (ct->verbs.v[k].attrs & QUEVU_VERB_DISABLED) != 0,
                                         self, @selector(tabVerb:));
            [tabs[i].page addSubview:ct->buttons[k]];
        }
    }
    [self layoutTab:c->id];
}

- (void)tabRemove:(uint32_t)id
{
    int i = [self tabIndex:id];
    tab gone_tab;

    if (i < 0)
        return;
    gone_tab = tabs[i];
    memmove(&tabs[i], &tabs[i + 1], (ntabs - (size_t)i - 1) * sizeof *tabs);
    ntabs--;
    if (front == id) {
        /* The neighbour comes to the front, if there is one. */
        int next = qv_tab_front_after_remove((size_t)i, ntabs);
        front = next >= 0 ? tabs[next].id : 0;
    }
    syncing = 1;
    [tabs_ctl removeTabViewItem:gone_tab.item];
    syncing = 0;
    [self freeTab:&gone_tab];
    [self syncFront];
    [self showCurrent];
}

/* The user chose a tab in the row. */
- (void)tabView:(NSTabView *)view didSelectTabViewItem:(NSTabViewItem *)item
{
    size_t i;
    (void)view;
    if (syncing || gone)
        return;
    for (i = 0; i < ntabs; i++)
        if (tabs[i].item == item) {
            front = tabs[i].id;
            [self showCurrent];
            if (tabs[i].is_source)
                [window makeFirstResponder:tabs[i].s.view];
            return;
        }
}

/* ---- menus ---- */

- (menu_item *)findItem:(uint32_t)id
{
    size_t i;
    for (i = 0; i < nitems; i++)
        if (items[i].id == id)
            return &items[i];
    return NULL;
}

/* An entry of one of the host's menus was chosen. */
- (void)menuChosen:(NSMenuItem *)item
{
    if (!gone)
        qv_emit(QUEVU_EV_MENU, (uint32_t)[item tag], 0, 0, 0);
}

/* Hangs from a menu the entries the host gave for it, and theirs. */
- (void)buildMenu:(NSMenu *)menu parent:(uint32_t)parent
{
    size_t i;

    [menu setAutoenablesItems:NO];
    for (i = 0; i < nitems; i++) {
        const menu_item *m = &items[i];
        NSMenuItem      *item;
        NSString        *words;

        if (m->parent != parent)
            continue;
        if (m->attrs & QUEVU_MENU_SEPARATOR) {
            [menu addItem:[NSMenuItem separatorItem]];
            continue;
        }
        words = menu_label(m->label ? m->label : @"");
        item = [menu addItemWithTitle:words action:NULL keyEquivalent:@""];
        if (m->attrs & QUEVU_MENU_SUBMENU) {
            NSMenu *sub = [[[NSMenu alloc] initWithTitle:words] autorelease];
            [self buildMenu:sub parent:m->id];
            [item setSubmenu:sub];
        } else {
            [item setTarget:self];
            [item setAction:@selector(menuChosen:)];
            [item setTag:m->id];
        }
        if (m->attrs & QUEVU_MENU_CHECKED)
            [item setState:NSControlStateValueOn];
        [item setEnabled:(m->attrs & QUEVU_MENU_DISABLED) == 0];
    }
}

/*
 * Writes the menu bar again from the entries the host has given. The
 * first menu of the bar is the one macOS names after the program; the
 * host's come after it. An entry the host put straight on the bar opens
 * a menu with that one entry, since here the bar holds only menus.
 */
- (void)rebuildMenus
{
    NSMenu     *bar = [[[NSMenu alloc] initWithTitle:@""] autorelease];
    NSMenuItem *first = [bar addItemWithTitle:@"" action:NULL keyEquivalent:@""];
    size_t      i;

    menus_dirty = 0;
    [first setSubmenu:[[[NSMenu alloc] initWithTitle:@""] autorelease]];
    for (i = 0; i < nitems; i++) {
        const menu_item *m = &items[i];
        NSString        *words;
        NSMenuItem      *top;
        NSMenu          *sub;

        if (m->parent != QUEVU_MENU_BAR || (m->attrs & QUEVU_MENU_SEPARATOR))
            continue;
        words = menu_label(m->label ? m->label : @"");
        top = [bar addItemWithTitle:words action:NULL keyEquivalent:@""];
        sub = [[[NSMenu alloc] initWithTitle:words] autorelease];
        if (m->attrs & QUEVU_MENU_SUBMENU) {
            [self buildMenu:sub parent:m->id];
        } else {
            NSMenuItem *only = [sub addItemWithTitle:words action:@selector(menuChosen:) keyEquivalent:@""];
            [sub setAutoenablesItems:NO];
            [only setTarget:self];
            [only setTag:m->id];
            if (m->attrs & QUEVU_MENU_CHECKED)
                [only setState:NSControlStateValueOn];
            [only setEnabled:(m->attrs & QUEVU_MENU_DISABLED) == 0];
        }
        [top setSubmenu:sub];
    }
    [NSApp setMainMenu:bar];
}

- (void)menuAdd:(qv_cmd *)c
{
    menu_item *m;
    if (nitems == items_cap) {
        items_cap = items_cap ? items_cap * 2 : 16;
        items = qv_grow(items, items_cap * sizeof *items);
    }
    m = &items[nitems++];
    m->id = c->id;
    m->parent = c->tab;
    m->label = [take_text(c) retain];
    m->attrs = c->b;
    menus_dirty = 1;
}

/* Takes out of the list everything that hangs from an entry. */
- (void)dropItemsUnder:(uint32_t)parent
{
    size_t k = 0;
    while (k < nitems) {
        if (items[k].parent == parent) {
            uint32_t id = items[k].id;
            [items[k].label release];
            memmove(&items[k], &items[k + 1], (nitems - k - 1) * sizeof *items);
            nitems--;
            [self dropItemsUnder:id];
            k = 0;
        } else {
            k++;
        }
    }
}

- (void)menuRemove:(uint32_t)id
{
    menu_item *m = [self findItem:id];
    if (!m)
        return;
    [m->label release];
    memmove(m, m + 1, (size_t)(&items[nitems] - (m + 1)) * sizeof *m);
    nitems--;
    [self dropItemsUnder:id];
    menus_dirty = 1;
}

/* ---- commands of the host ---- */

/*
 * A text changed: whatever shows it on a tab that already exists is
 * written again.
 */
- (void)applyTexts
{
    size_t i;
    for (i = 0; i < ntabs; i++) {
        if (tabs[i].is_source) {
            source *s = &tabs[i].s;
            [s->find_edit setPlaceholderString:[self textOf:QUEVU_TEXT_FIND_CUE]];
            [s->history setTitle:[self textOf:QUEVU_TEXT_HISTORY]];
            [s->from_label setStringValue:[self textOf:QUEVU_TEXT_FROM]];
            [s->to_label setStringValue:[self textOf:QUEVU_TEXT_TO]];
            [self layoutTab:tabs[i].id];
        }
        [self refreshStrip:(int)i];
    }
}

/* Does what a command says. Returns 1 if the window is gone with it. */
- (int)apply:(qv_cmd *)c
{
    int        i;
    source    *s;
    QVTable   *t;
    menu_item *m;

    switch (c->kind) {
    case QV_CONSOLE_ADD:
        [self addSource:c->id title:take_text(c) weight:c->weight];
        break;
    case QV_CONTROLS_ADD:
        [self addControls:c->id title:take_text(c) weight:c->weight];
        break;
    case QV_TAB_TITLE:
        if ((i = [self tabIndex:c->id]) >= 0) {
            sset(&tabs[i].title, take_text(c));
            [self retitle:(size_t)i];
        }
        break;
    case QV_TAB_SELECT:
        if ([self tabIndex:c->id] >= 0) {
            front = c->id;
            [self syncFront];
            [self showCurrent];
        }
        break;
    case QV_TAB_REMOVE:
        [self tabRemove:c->id];
        break;
    case QV_TAB_NOTE:
        if ((i = [self tabIndex:c->id]) >= 0) {
            sset(&tabs[i].note, take_text(c));
            /* The bar of a control tab comes and goes with it. */
            [self layoutTab:c->id];
            [self refreshStrip:i];
        }
        break;
    case QV_TAB_VERBS:
        [self tabVerbs:c];
        break;
    case QV_CONTROL_ADD:
        i = [self tabIndex:c->tab];
        if (i >= 0 && !tabs[i].is_source)
            [tabs[i].c.table add:c->id name:take_text(c)];
        break;
    case QV_CONTROL_STATE:
        if ((t = [self tableOf:c->id]) != nil)
            [t set:c->id rgb:c->a attrs:c->b text:take_text(c) verbs:&c->verbs];
        break;
    case QV_CONTROL_REMOVE:
        if ((t = [self tableOf:c->id]) != nil)
            [t remove:c->id];
        break;
    case QV_DELIVER: {
        /*
         * New output on a tab that is not in front leaves a mark on its
         * title.
         */
        int fresh = c->a == QUEVU_OP_LIVE && c->nlines > 0;
        i = [self tabIndex:c->id];
        if (i < 0)
            break;
        if ((s = [self sourceAt:i]) != NULL) {
            view_deliver(s->view, c->a, c->full, c->history, c->lines, c->nlines);
            c->nlines = 0;
        }
        if (fresh && [self current] != i && !tabs[i].unseen) {
            tabs[i].unseen = 1;
            [self retitle:(size_t)i];
        }
        break;
    }
    case QV_CONSOLE_TIME:
        if ((s = [self sourceAt:[self tabIndex:c->id]]) != NULL) {
            s->show_time = c->full;
            s->view->show_time = c->full;
            [s->view changed];
        }
        break;
    case QV_GOTO:
        if ((s = [self sourceAt:[self tabIndex:c->id]]) != NULL) {
            view_goto(s->view, c->lines, c->nlines, c->seq, c->history);
            c->nlines = 0;
        }
        break;
    case QV_FIND_STATUS:
        i = [self tabIndex:c->id];
        if ((s = [self sourceAt:i]) != NULL) {
            sset(&s->find_status, take_text(c));
            s->find_resume = c->seq;
            s->find_offer = c->history;
            [self refreshStrip:i];
        }
        break;
    case QV_MENU_ADD:
        [self menuAdd:c];
        break;
    case QV_MENU_SET:
        if ((m = [self findItem:c->id]) != NULL) {
            sset(&m->label, take_text(c));
            /*
             * Whether an entry opens a menu is settled when it is added;
             * the rest can change.
             */
            m->attrs = (c->b & ~(uint32_t)QUEVU_MENU_SUBMENU) | (m->attrs & QUEVU_MENU_SUBMENU);
            menus_dirty = 1;
        }
        break;
    case QV_MENU_REMOVE:
        [self menuRemove:c->id];
        break;
    case QV_TEXT:
        if (c->id < QV_TEXT_COUNT) {
            sset(&texts[c->id], take_text(c));
            [self applyTexts];
        }
        break;
    case QV_TRAY:
        /* No icon in the menu bar of the system here: see the top of this file. */
        break;
    case QV_SHOW:
        if (c->full) {
            [window makeKeyAndOrderFront:nil];
            [NSApp activateIgnoringOtherApps:YES];
        } else {
            [window orderOut:nil];
        }
        break;
    case QV_QUIT:
        /* The window goes, and the loop with it. */
        gone = 1;
        [window orderOut:nil];
        [NSApp stop:nil];
        /* The loop looks at whether to stop only after an event. */
        [NSApp postEvent:[NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                            location:NSZeroPoint
                                       modifierFlags:0
                                           timestamp:0
                                        windowNumber:0
                                             context:nil
                                             subtype:0
                                               data1:0
                                               data2:0]
                 atStart:YES];
        return 1;
    default:
        break;
    }
    return 0;
}

/* Applies everything the host has queued. Returns 1 if the window is gone. */
- (int)drain:(int)starting
{
    qv_cmd *c = qv_drain();
    int     ended = 0;

    while (c) {
        qv_cmd *next = c->next;
        /*
         * A quit takes the window away: what came after it is dropped.
         * One asked for while no window was open does nothing: it must
         * not close a window that came afterwards.
         */
        if (!ended && !(starting && c->kind == QV_QUIT))
            ended = [self apply:c];
        qv_cmd_free(c);
        c = next;
    }
    if (!ended && menus_dirty)
        [self rebuildMenus];
    return ended;
}

/* ---- the window ---- */

/*
 * Closing is the host's decision: it hears the request and, when it sees
 * fit, tells this window to go.
 */
- (BOOL)windowShouldClose:(NSWindow *)sender
{
    (void)sender;
    qv_emit(QUEVU_EV_CLOSE, 0, 0, 0, 0);
    return NO;
}

/*
 * The keys of the search work wherever the keyboard is: in the console,
 * in the search box, on a tab. They are taken on the way in, so no view
 * has to know about them. Returns whether the key was one of them.
 */
- (BOOL)searchKey:(NSEvent *)ev
{
    NSString *chars = [ev charactersIgnoringModifiers];
    unichar   c = [chars length] > 0 ? [chars characterAtIndex:0] : 0;
    BOOL      shift = ([ev modifierFlags] & NSEventModifierFlagShift) != 0;
    BOOL      cmd = ([ev modifierFlags] & NSEventModifierFlagCommand) != 0;
    int       id = 0;

    if (gone || [ev window] != window)
        return NO;
    if (c == NSF3FunctionKey)
        id = shift ? ID_FIND_NEWER : ID_FIND_OLDER;
    else if (cmd && (c == 'f' || c == 'F'))
        id = ID_FIND;
    if (!id || [self current] < 0)
        return NO;
    [self sourceCommand:[self current] what:id];
    return YES;
}

- (id)initWithTitle:(NSString *)title
{
    NSRect frame = NSMakeRect(0, 0, 1040, 660);

    self = [super init];
    if (!self)
        return nil;
    window = [[NSWindow alloc]
        initWithContentRect:frame
                  styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable |
                            NSWindowStyleMaskResizable
                    backing:NSBackingStoreBuffered
                      defer:NO];
    /* The window is let go of here, not by the system when it closes. */
    [window setReleasedWhenClosed:NO];
    [window setTitle:title];
    [window setDelegate:self];
    [window center];

    /*
     * The window starts with the row of tabs and nothing in it: no tab,
     * no menu. Both are the host's to add.
     */
    tabs_ctl = [[NSTabView alloc] initWithFrame:NSInsetRect(frame, 8, 8)];
    [tabs_ctl setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable];
    [tabs_ctl setDelegate:self];
    [tabs_ctl setHidden:YES];
    [[window contentView] addSubview:tabs_ctl];
    return self;
}

- (void)dealloc
{
    size_t i;
    if (key_monitor) {
        [NSEvent removeMonitor:key_monitor];
        [key_monitor release];
    }
    [tabs_ctl setDelegate:nil];
    [window setDelegate:nil];
    for (i = 0; i < ntabs; i++) {
        [tabs_ctl removeTabViewItem:tabs[i].item];
        [self freeTab:&tabs[i]];
    }
    free(tabs);
    for (i = 0; i < nitems; i++)
        [items[i].label release];
    free(items);
    for (i = 0; i < QV_TEXT_COUNT; i++)
        [texts[i] release];
    [tabs_ctl release];
    [window close];
    [window release];
    [super dealloc];
}

@end

/* ---- the lanes ---- */

void qv_cocoa_wake(void)
{
    /* One wake at a time is enough: it drains everything there is. */
    if (!__sync_bool_compare_and_swap(&wake_pending, 0, 1))
        return;
    dispatch_async(dispatch_get_main_queue(), ^{
        QVController *a = the_app;
        __sync_lock_release(&wake_pending);
        if (a && !a->gone)
            [a drain:0];
    });
}

/*
 * Whether a window can be opened: the program runs inside a session of
 * the desktop. One started from a remote shell with nobody logged in at
 * the screen has none.
 */
int qv_cocoa_available(void)
{
    CFDictionaryRef session = CGSessionCopyCurrentDictionary();
    if (!session)
        return 0;
    CFRelease(session);
    return 1;
}

int qv_win_run(const uint16_t *title)
{
    if (!qv_cocoa_available())
        return -1;
    @autoreleasepool {
        QVController *a;

        [NSApplication sharedApplication];
        /* A program with a window of its own, a place in the Dock and a menu bar. */
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];

        __sync_lock_release(&wake_pending);
        a = [[QVController alloc] initWithTitle:str(title)];
        the_app = a;
        a->key_monitor = [[NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown
                                                               handler:^NSEvent *(NSEvent *ev) {
                                                                   return [the_app searchKey:ev] ? nil : ev;
                                                               }] retain];
        /* What is applied below already has things to tell the host. */
        qv_begin();
        /*
         * Whatever the host queued before the window existed is applied
         * before the window is shown: it opens with its tabs and its
         * menu, not empty and then filling up.
         */
        [a drain:1];
        [a rebuildMenus];
        [a->window makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];

        /* From here on the host can reach this thread. */
        qv_attach(a->window);
        qv_emit(QUEVU_EV_READY, 0, 0, 0, 0);
        /* Whatever the host queued in the meantime. */
        qv_cocoa_wake();

        [NSApp run];

        qv_detach();
        the_app = nil;
        [a release];
    }
    return 0;
}
