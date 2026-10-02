// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "Encody",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "Encody",
            path: "Sources/Encody"
        )
    ]
)
