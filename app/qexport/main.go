package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/doc"
	"go/format"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	flagExportPath     string
	flagDefaultContext bool
	flagCustomContext  string
)

const help = `Export go packages to qlang modules.

Usage:
  qexport [-contexts=""] [-defctx=false] [-outpath="./qlang"] packages

The packages for go package list or std for golang all standard packages.
`

func usage() {
	fmt.Fprintln(os.Stderr, help)
	flag.PrintDefaults()
}

func init() {
	flag.StringVar(&flagExportPath, "outpath", "./qlang", "optional set export root path")
	flag.BoolVar(&flagDefaultContext, "defctx", true, "optional use default context for build, default use all contexts.")
	flag.StringVar(&flagCustomContext, "contexts", "", "optional comma-separated list of <goos>-<goarch>[-cgo] to override default contexts.")
}

func main() {
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		usage()
		return
	}

	if flagCustomContext != "" {
		flagDefaultContext = false
		setCustomContexts(flagCustomContext)
	}

	var outpath string
	if filepath.IsAbs(flagExportPath) {
		outpath = flagExportPath
	} else {
		dir, err := os.Getwd()
		if err != nil {
			log.Fatalln(err)
		}
		outpath = filepath.Join(dir, flagExportPath)
	}

	var pkgs []string
	if args[0] == "std" {
		out, err := exec.Command("go", "list", "-e", args[0]).Output()
		if err != nil {
			log.Fatal(err)
		}
		pkgs = strings.Fields(string(out))
	} else {
		pkgs = args
	}

	for _, pkg := range pkgs {
		err := export(pkg, outpath, true)
		if err != nil {
			log.Printf("export pkg %q error, %s.\n", pkg, err)
		} else {
			log.Printf("export pkg %q success.\n", pkg)
		}
	}
}

var (
	skip_const_keys = []string{
		"crc64.ECMA",
		"crc64.ISO",
		"math.MaxUint64",
	}
)

func isSkipConst(key string) bool {
	for _, k := range skip_const_keys {
		if key == k {
			return true
		}
	}
	return false
}

func export(pkg string, outpath string, skipOSArch bool) error {
	p, err := NewPackage(pkg, flagDefaultContext)
	if err != nil {
		return err
	}

	p.Parser()

	bp := p.BuildPackage()
	if bp == nil {
		return errors.New("not find build")
	}

	if p.CommonCount() == 0 {
		return errors.New("empty common exports")
	}

	if pkg == "unsafe" {
		return errors.New("skip unsafe pkg")
	}

	pkgName := bp.Name

	//skip internal
	for _, path := range strings.Split(bp.ImportPath, "/") {
		if path == "internal" {
			return errors.New("skip internal pkg")
		}
	}

	var buf bytes.Buffer
	outf := func(format string, a ...interface{}) (err error) {
		_, err = buf.WriteString(fmt.Sprintf(format, a...))
		return
	}

	//write package
	outf("package %s\n", pkgName)

	//write imports
	outf("import (\n")
	outf("\t%q\n", pkg)
	outf(")\n\n")

	//write exports
	outf(`// Exports is the export table of this module.
//
var Exports = map[string]interface{}{
	"_name": "%s",	
`, pkg)

	var addins []string
	//const
	if keys, _ := p.FilterCommon(Const); len(keys) > 0 {
		outf("\n")
		for _, v := range keys {
			name := toQlangName(v)
			fn := pkgName + "." + v
			if isSkipConst(fn) {
				log.Println("waring skip const", fn)
				continue
			}
			outf("\t%q:\t%s,\n", name, fn)
		}
	}

	//vars
	if keys, _ := p.FilterCommon(Var); len(keys) > 0 {
		outf("\n")
		for _, v := range keys {
			name := toQlangName(v)
			fn := pkgName + "." + v
			outf("\t%q:\t%s,\n", name, fn)
		}
	}

	//funcs
	if keys, _ := p.FilterCommon(Func); len(keys) > 0 {
		outf("\n")
		for _, v := range keys {
			name := toQlangName(v)
			fn := pkgName + "." + v
			outf("\t%q:\t%s,\n", name, fn)
		}
	}

	//structs
	if keys, m := p.FilterCommon(Struct); len(keys) > 0 {
		_, fm := p.FilterCommon(Factor)
		outf("\n")
		for _, v := range keys {
			t, ok := m[v]
			if !ok {
				continue
			}
			dt, ok := t.(*doc.Type)
			if !ok {
				continue
			}
			//empty func
			if len(dt.Funcs) == 0 {
				//fmt.Println(v)
				name := toQlangName(v)
				var vfn string = "var" + v
				var tname string = pkgName + "." + v
				addins = append(addins, fmt.Sprintf("func %s() %s {\n\tvar v %s\n\treturn v\n}",
					vfn, tname, tname,
				))
				var vfns string = "new" + v + "Array"
				addins = append(addins, fmt.Sprintf("func %s(n int) []%s {\n\treturn make([]%s,n)\n}",
					vfns, tname, tname,
				))
				outf("\t%q:\t%s,\n", name, vfn)
				outf("\t%q:\t%s,\n", name+"Array", vfns)
			} else {
				//write factor func and check is common
				for _, f := range dt.Funcs {
					if _, ok = fm[f.Name]; ok {
						name := toQlangName(f.Name)
						fn := pkgName + "." + f.Name
						outf("\t%q:\t%s,\n", name, fn)
					}
				}
			}
		}
	}

	// end exports
	outf("}")

	if len(addins) > 0 {
		for _, addin := range addins {
			outf("\n\n")
			outf(addin)
		}
	}

	// format
	data, err := format.Source(buf.Bytes())
	if err != nil {
		return err
	}

	// write file
	root := filepath.Join(outpath, pkg)
	err = os.MkdirAll(root, 0777)
	if err != nil {
		return err
	}

	file, err := os.Create(filepath.Join(root, pkgName+".go"))
	if err != nil {
		return err
	}
	defer file.Close()
	file.Write(data)

	return nil
}

func toQlangName(s string) string {
	if len(s) <= 1 {
		return s
	}

	if unicode.IsLower(rune(s[1])) {
		return strings.ToLower(s[0:1]) + s[1:]
	}
	return s
}
