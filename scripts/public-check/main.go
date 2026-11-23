// Package main проверяет публичное дерево «Диктовки» перед выпуском.
//
// Контракт: белый список release/public-tree.txt задаёт состав дерева; всё
// остальное считается приватным и в выпуск не попадает. Внутри белого списка
// запрещено ВСЁ, что перечисляет publictree.ForbiddenNeedles, — без
// исключений. Прежние исключения для токенов протокола и путей kits сняты
// решением владельца (19.11): публичное дерево не должно содержать
// запрещённую подстроку ни в каком виде — нужные части kits попадают в него
// трансформацией.
//
// Вызов: go run ./scripts/public-check [-root каталог] [-list].
// Код 0 — нарушений нет, 1 — есть нарушения, 2 — не удалось прочитать список.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/publictree"
)

func main() {
	root := flag.String("root", ".", "корень проверяемого дерева")
	list := flag.Bool("list", false, "печатать список публичных файлов")
	flag.Parse()

	patterns, err := publictree.LoadPatterns(filepath.Join(*root, "release", "public-tree.txt"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "public-check:", err)
		os.Exit(2)
	}
	public, all, found, err := publictree.Check(*root, patterns)
	if err != nil {
		fmt.Fprintln(os.Stderr, "public-check:", err)
		os.Exit(2)
	}
	if *list {
		for _, path := range public {
			fmt.Println(path)
		}
		return
	}

	for _, one := range found {
		fmt.Printf("%s:%d: запрещённая строка %q\n", one.Path, one.Line, one.Needle)
	}
	fmt.Printf("public-check: файлов в дереве %d, публичных %d, нарушений %d\n", all, len(public), len(found))
	if len(found) > 0 {
		os.Exit(1)
	}
}
