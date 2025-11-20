# go-lrzsz

A Go implementation of the ZModem file transfer protocol.

Converted from the C `lrzsz` (v0.12.20) codebase with the aid of LLM tools.

## Status

Parts work, parts probably don't. The project was an experiment in using an LLM to convert the C code over to Go, then wrap it up in a library so it can be used in SSH based projects. Not all the code is LLM code, but the majority is.

Even given the working C code from `lrzsz`, the LLM struggled to get things right, making weird assumptions and making changes because it went down a rabbit hole disregarding the original code. Human intervention was required numerous times to correct the code and explain why something was not how it should be.

## What Works

### Library Mode

* A wrapped SSH connection can send a file when the remote invokes "rz"
* Supports streamed mode and ACK mode (though the latter is VERY slow depending on your settings)

## Everything Else

Untested, likely needs work.
