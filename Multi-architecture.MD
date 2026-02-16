X64 only!!!!


As soon as we hit v5 blocks (Hoohash V110) the hash calculation (in some cases) diverges when running on Linux/aarch64.

Neither go build -gcflags=all="-N -l" nor go build -gcflags="all=-m=2 -d=ssa/late_fuse=0"  help.


Without switching to a full carefully written, FMA preventing, C implementation of PoWHash, we are stuck with just X64 support for the node for the time being.

It might not be FMA, it could also be libm variants of sin and other transcendentals - needs an big test harness to fully validate.  Any such conversion would inevitably not be bitwise deterministically compatbile with the current hash function, and thus need a HF.

Suggest low priority.

Occasional divergence in the miner is tolerable, in the node, clearly not.
