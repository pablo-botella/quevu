#!/bin/sh
# Writes qv_gtk_syms_linux.h: the list of the functions of GTK and of what
# comes with it that the Linux side of the window calls, one for every
# G(...) in its sources. Run it in this folder after adding a call.
set -e
out=qv_gtk_syms_linux.h
bs='\'
{
    echo '/* Written by qv_gtk_syms.sh from every G(...) in the sources. Do not edit. */'
    echo '#ifndef QV_GTK_SYMS_LINUX_H'
    echo '#define QV_GTK_SYMS_LINUX_H'
    echo
    echo "#define QV_GTK_SYMS(X) $bs"
    cat qv_*_linux.c qv_gtk_linux.h | grep -o 'G([a-z_0-9]*)' | tr -d 'G()' | grep -v '^f$' | sort -u |
        while read -r name; do
            echo "    X($name) $bs"
        done
    echo '    /* end */'
    echo
    echo '#endif /* QV_GTK_SYMS_LINUX_H */'
} >"$out"
