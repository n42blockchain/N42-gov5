// SPDX-License-Identifier: LGPL-3.0-or-later
//
// MinimalWrapper — Swift binding for the MOBILE MINIMAL stateless client facade
// (cmd/evmsdk/mobile_minimal.go), exposed by gomobile as EvmsdkMobileMinimal*.
//
// Three trust layers, phone-sized:
//   ① header chain  — EvmsdkMobileMinimalSync(To): extend parentHash to tip.
//   ③ per-account   — EvmsdkMobileBalanceOf: bounded EIP-1186 proof (~KB) verified
//                      against the header-chain-trusted stateRoot.
//   (② witness EVM replay reuses the existing verifier path.)
//
// All facade calls return "" on success / JSON on query / "error: ..." on failure.

import Foundation
import Combine
import Evmsdk

@MainActor
final class MinimalWrapper: ObservableObject {

    // MARK: Published state (SwiftUI)
    @Published var status      = "Idle"
    @Published var initialized  = false
    @Published var head: Int64  = 0
    @Published var headHash     = ""
    @Published var trustBlock: Int64 = 0
    @Published var trustHash    = ""
    @Published var retained     = 0
    @Published var reanchors    = 0
    @Published var syncing      = false
    @Published var syncProgress = 0.0          // 0..1 over the catch-up span
    @Published var tip: Int64   = 0
    @Published var accounts: [Account] = []
    @Published var lastProofBytes = 0
    @Published var lastVerify = ""          // ② witness-replay result line

    struct Account: Identifiable {
        var id: String { address }
        let address: String
        let exists: Bool
        let balanceWei: String
        let nonce: Int64
        let block: Int64
        let proofBytes: Int
        let verified: Bool
    }

    // MARK: Bootstrap
    /// Trust a social checkpoint {block, hash} and follow `idcURL`. retention ~=
    /// 7200 for a day of 12s blocks.
    func initialize(idcURL: String, checkpointBlock: Int64, checkpointHash: String, retention: Int64 = 7200) {
        let err = EvmsdkMobileMinimalInit(idcURL, checkpointBlock, checkpointHash, retention)
        if !err.isEmpty { status = "Init failed: \(err)"; initialized = false; return }
        initialized = true
        status = "Anchored @ \(checkpointBlock)"
        apply(stateJSON: EvmsdkMobileMinimalState())
    }

    // MARK: Sync (chunked, with progress)
    /// Catches up to the IDC tip in bounded steps so the UI animates + the phone
    /// never does one huge sync. Safe to call on a 12s ticker once at tip.
    func sync(chunk: Int64 = 2048) async {
        guard initialized, !syncing else { return }
        syncing = true; status = "Syncing ①"
        defer { syncing = false }

        // Discover tip via a no-op state probe (the facade syncs to tip on Sync()).
        let start = head
        var target = head
        // Step the cap upward; MobileMinimalSyncTo returns the new state each step.
        while true {
            target += chunk
            let js = EvmsdkMobileMinimalSyncTo(target)
            if js.hasPrefix("error:") { status = js; return }
            apply(stateJSON: js)
            if tip > 0 { syncProgress = min(1.0, Double(head - start) / Double(max(1, tip - start))) }
            await Task.yield()
            if head < target { break }            // reached the real tip (capped below target)
        }
        // Final uncapped sync to land exactly on tip.
        apply(stateJSON: EvmsdkMobileMinimalSync())
        syncProgress = 1.0
        status = "At tip"
    }

    // MARK: Per-account trustless balance (③)
    func verifyBalance(_ address: String) {
        let js = EvmsdkMobileBalanceOf(address)
        if js.hasPrefix("error:") { status = js; return }
        guard let d = json(js) else { return }
        let acc = Account(
            address: d["address"] as? String ?? address,
            exists: d["exists"] as? Bool ?? false,
            balanceWei: d["balance"] as? String ?? "0",
            nonce: (d["nonce"] as? NSNumber)?.int64Value ?? 0,
            block: (d["block"] as? NSNumber)?.int64Value ?? 0,
            proofBytes: (d["proofBytes"] as? NSNumber)?.intValue ?? 0,
            verified: d["verified"] as? Bool ?? false
        )
        lastProofBytes = acc.proofBytes
        accounts.removeAll { $0.address.lowercased() == acc.address.lowercased() }
        accounts.insert(acc, at: 0)
    }

    // MARK: Layer ② — on-device witness EVM replay for a block
    func verifyBlock(_ n: Int64) {
        let js = EvmsdkMobileVerifyBlock(n)
        if js.hasPrefix("error:") { lastVerify = "② \(n): \(js)"; return }
        guard let d = json(js) else { return }
        let ok = d["verified"] as? Bool ?? false
        let tx = (d["txCount"] as? NSNumber)?.intValue ?? 0
        lastVerify = "② block \(n): \(ok ? "✓ verified" : "✗ failed") · \(tx) tx"
    }

    func free() { _ = EvmsdkMobileMinimalFree(); initialized = false }

    // MARK: helpers
    private func apply(stateJSON js: String) {
        guard let d = json(js) else { return }
        head      = (d["head"] as? NSNumber)?.int64Value ?? head
        headHash  = d["hash"] as? String ?? headHash
        retained  = (d["retained"] as? NSNumber)?.intValue ?? retained
        reanchors = (d["reanchors"] as? NSNumber)?.intValue ?? reanchors
        if let tr = d["trustRoot"] as? [String: Any] {
            trustBlock = (tr["block"] as? NSNumber)?.int64Value ?? trustBlock
            trustHash  = tr["hash"] as? String ?? trustHash
        }
        if head > tip { tip = head }
    }

    private func json(_ s: String) -> [String: Any]? {
        guard let data = s.data(using: .utf8) else { return nil }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
    }
}
