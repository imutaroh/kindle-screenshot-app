// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "PageSnap",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "PageSnap",
            path: "Sources/PageSnap"
        )
    ]
)
