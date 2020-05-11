#perl -0777 -i -p 's+go func\(\)\{+\/\/go func\(\) \{/g' go/border/router.go
#perl -i s+Start\(\)+//Start\(\)+g router.go
#sed -i s+Start\(\)+//Start\(\)+g go/border/router.go
#sed -i 's+go\sfunc\(\)\s\{+\/\/\sgo\sfunc\(\)\s\{+g' go/border/router.go
#sed -i '{N; s+defer log\.HandlePanic()\n\sr\.bscNotify()+\/\/defer log\.HandlePanic()\n\s\/\/r\.bscNotify()+g}' go/border/router.go
#sed -i -e '{N; s+\}\selse\s\{\nbreak+\}\selses\s\{\nbreak+g}' go/border/router.go
#sed -i '{N; s+hello {}()\n.*world+how \/\/\nare\nyou+g}' go/border/test.txt
#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.bscNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.stochNotify()+g'} go/border/router.go

#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.stochNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.stochNotify()+g'} go/border/router.go
#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.bscNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.bscNotify()+g'} go/border/router.go

#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.bscNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.bscNotify()+g'} go/border/router.go

#./scion.sh build 

sed -i '{N; s+.*\/\/.*defer log\.HandlePanic()\n.*\/\/.*r\.bscNotify()+\t\tdefer log\.HandlePanic()\n\t\tr\.bscNotify()+g}' go/border/router.go


sed -i '{N; s+.*\/\/.*defer log\.HandlePanic()\n.*\/\/.*r\.stochNotify()+\t\tdefer log\.HandlePanic()\n\t\tr\.stochNotify()+g}' go/border/router.go

#sed -i '{N; s+.*\/\/.*defer log\.HandlePanic()+\t\tdefer log\.HandlePanic()+g}' go/border/router.go
