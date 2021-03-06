ews-proxy
=========

Reverse proxy that allows using an EWS XML client on an OWA endpoint.

Compilation requirements
------------------------

Despite this being a golang package, there is an autogenerated piece that is
written using Python. You must have python 2 installed, and you must have
xmlschema 0.9.9 installed. On Windows:

    py -2 -m pip install xmlschema==0.9.9

On Linux/OSX:

    python2 -m pip install xmlschema==0.9.9

Once that is installed, you should be able to run `go generate` to generate the
needed files.

Compilation
-----------

This package requires autogenerated pieces, which can be generated using the
'go generate' command.

    go generate
    go install

If you are using this package as a library, then you would need to run the
following from your application source code directory:

    go get
    go generate github.com/virtuald/ews-proxy



