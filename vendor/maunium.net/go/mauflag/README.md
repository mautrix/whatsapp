# mauflag
[![Build Status](http://img.shields.io/travis/tulir293/mauflag.svg?style=flat-square)](https://travis-ci.org/tulir293/mauflag)
[![License](http://img.shields.io/:license-gpl3-blue.svg?style=flat-square)](http://www.gnu.org/licenses/gpl-3.0.html)

An extendable argument parser for Golang. Mostly follows the GNU [Program Argument Syntax Conventions](https://www.gnu.org/software/libc/manual/html_node/Argument-Syntax.html).

Use `go get maunium.net/go/mauflag` to get this package

## Basic usage
A flag can have as many long or short keys as you like. Short keys are prefixed with one dash (`-`) whilst long keys are prefixed with two (`--`)

To create a flag, you must first call the `Make()` method in a flagset. Calling flagset methods as package functions uses the default flagset which takes input arguments from `os.Args`

After creating a flag, you can call the functions it has as you like. After configuring the flag, you can call one of the type functions to set the flag type and get a pointer to the value.

### Examples
Here's a basic string flag that can be set using `-s` or `--my-string-flag`
```go
var myStringFlag = flag.Make().ShortKey("s").LongKey("my-string-flag").Default("a string").String()
```

The following input arguments would set the value of the flag to `foo`
* `-s`, `foo`
* `-s=foo`
* `-sfoo`
* `--my-string-flag`, `foo`
* `--my-string-flag=foo`

#### Chaining
Short boolean flags can be chained. The last flag of a chain doesn't necessarily have to be a boolean flag.
For example, if we had the following flags defined
```go
var boolA = flag.Make().ShortKey("a").Bool()
var boolB = flag.Make().ShortKey("b").Bool()
var boolC = flag.Make().ShortKey("c").Bool()
var stringD = flag.Make().ShortKey("d").String()
```
We could set the values of all the boolean flags to true with one input argument: `-abc`. We could also add the `d` flag to the end like so: `-abcd`, `foo`.
Any of the other ways to set short flags also work (`-abcd=foo`, `-abcdfoo`).

However, if you put `d` as the first flag (`-dabc`) the value of the `d` flag will be `abc` and none of the boolean flags would change.

### Godoc
More docs, including all values supported by default, can be found from [godoc.org/maunium.net/go/mauflag](https://godoc.org/maunium.net/go/mauflag)

## Custom values
All value containers must implement the `Value` interface. It contains two functions:
* `Set(string)` which is called whenever the parser finds a value associated with the flag.
* `Name()` which should return a human-readable name for the type of the value. If the value container contains multiple actual values, `Name()` should return the name of a single object (e.g. `integer` if the value container contains multiple integers)

After creating a value container you can use it by calling the `Custom` method of a `Flag` and giving an instance of the value container. The parser will then call the `Set` method each time it encounters a value associated with the flag.
