# Hoosat Network Daemon

[![ISC License](http://img.shields.io/badge/license-ISC-blue.svg)](https://choosealicense.com/licenses/isc/)  
[![GoDoc](https://img.shields.io/badge/godoc-reference-blue.svg)](http://godoc.org/github.com/hoosatnet/htnd)

Hoosat Network Daemon is the reference full node implementation for Hoosat Network, written in Go (Golang).

## CONTRIBUTING

- All contributes via well formed Pull Requests against the master branch are welcome.  Please ensure that each PR only deals with a single issue.
- No other submission mechanism will be considered.
- If you identify an issue, but are not a developer, then please raise it as an issue here on GitHub.
- Continued reference to perceived issues without code submissions/opened issues will gain zero attention. 

## What is Hoosat Network?

Hoosat Network is an advanced cryptocurrency built with an ASIC-resistant proof-of-work (PoW) algorithm. It offers instant confirmations and sub-second block times, designed for both security and performance. Hoosat Network is a fork of Kaspa and utilizes the [DAGKnight protocol](https://eprint.iacr.org/2022/1494.pdf), an evoluation of GhostDAG and generalization of the Nakamoto consensus.

One of the most distinctive features of Hoosat Network is its unique PoW security. Hoosat Network is **the first and only cryptocurrency** that integrates advanced protection against nonce-guessing attacks of any level, including those that could potentially be launched using Grover's algorithm in quantum computing. This security is enabled by our patent pending technology, **"Securing Proof-of-Work Integrity,"** which sets Hoosat Network apart as a truly quantum-resistant cryptocurrency.

Hoosat Network is open-source, but it also includes patent pending technology to ensure its security, making it an exceptional and innovative project in the blockchain space.

## Key Features

- **ASIC-resistant**: Designed to resist specialized mining hardware (ASICs), ensuring that mining remains decentralized and accessible.
- **Quantum-resistant PoW**: The first cryptocurrency to offer PoW security against nonce-guessing attacks, including resistance to quantum algorithms like Grover’s algorithm.
- **Fast Block Times**: Sub-second block times and instant transaction confirmations, making Hoosat Network highly efficient and scalable.
- **Open Source**: Hoosat Network is fully open-source, allowing community contributions and transparent development.

## Requirements

Go 1.27.1 or later.

## Installation

#### Build from Source (Linux)

- Install Go according to the installation instructions here:
  http://golang.org/doc/install

- Ensure Go was installed properly and is a supported version:

```bash
$ go version
```

- Run the following commands to obtain and install htnd including all dependencies:

```bash
$ git clone https://github.com/HoosatNetwork/HTND
$ cd HTND
$ go install -ldflags="-s -w" --tags="pebblegozstd" . ./cmd/...
```

- HTND (and utilities) should now be installed in `$(go env GOPATH)/bin`. If you did
  not already add the bin directory to your system path during Go installation,
  you are encouraged to do so now.

#### Build from Source (Windows)

- Install Go according to the installation instructions here:
  http://golang.org/doc/install
  Download from - https://go.dev/dl/

- Install TDM GCC
  https://jmeubank.github.io/tdm-gcc/download/

- Ensure Go was installed properly and is a supported version:

```bash
$ go version
```

- Run the following commands to obtain and install htnd including all dependencies:

```bash
git clone https://github.com/HoosatNetwork/HTND
cd HTND
go install -ldflags="-s -w" --tags="deadlock pebblegozstd" . ./cmd/... 
```

- HTND (and utilities) should now be installed in `$(go env GOPATH)/bin`. If you did
  not already add the bin directory to your system path during Go installation,
  you are encouraged to do so now.

## Getting Started

HTND has several configuration options available to tweak how it runs, but all
of the basic operations work with zero configuration.

```bash
$ htnd
```

## Discord

<!-- TODO: the invite link is missing. Add the project's real Discord invite here; it was not
     recorded anywhere in this repository, and inviting people to a guessed URL would be worse
     than an honest gap. -->

The Discord invite link is <A HREF='https://discord.gg/VQsraB2Y8'>here</a>

## Issue Tracker

The [integrated github issue tracker](https://github.com/HoosatNetwork/HTND/issues)
is used for this project.

## Documentation

The [documentation](https://github.com/Hoosat-Oy/docs) is a work-in-progress

## License

HTND is licensed under the copyfree [ISC License](https://choosealicense.com/licenses/isc/).
