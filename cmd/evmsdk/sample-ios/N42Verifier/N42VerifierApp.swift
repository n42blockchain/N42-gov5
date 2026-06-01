// SPDX-License-Identifier: LGPL-3.0-or-later
// N42 Verifier — minimal SwiftUI sample app for the parallel verification track.

import SwiftUI

@main
struct N42VerifierApp: App {
    var body: some Scene {
        WindowGroup {
            TabView {
                MinimalView()
                    .tabItem { Label("Minimal", systemImage: "shield.lefthalf.filled") }
                ContentView()
                    .tabItem { Label("Verifier", systemImage: "checkmark.seal") }
            }
        }
    }
}
