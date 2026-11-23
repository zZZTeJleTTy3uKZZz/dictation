/* config.h — то немногое, что обычная сборка libopus получает от autoconf.
 *
 * Здесь только версия: она попадает в opus_get_version_string(), по ней видно,
 * какая библиотека собрана в двоичный файл. Остальные возможности (lrintf,
 * встроенные функции счёта битов) намеренно не объявлены: без них libopus
 * считает переносимым кодом, а это надёжнее, чем угадывать, что есть в mingw.
 */
#ifndef OPUSENC_CONFIG_H
#define OPUSENC_CONFIG_H

#define PACKAGE_VERSION "1.4"

#endif
