// Copyright (c) 2024 Hoosat Oy
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dagconfig

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var log = logger.RegisterSubSystem("DAGC")
var spawn = panics.GoroutineWrapperFunc(log)
