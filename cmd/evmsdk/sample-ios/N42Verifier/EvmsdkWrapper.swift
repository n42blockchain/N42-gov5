// SPDX-License-Identifier: LGPL-3.0-or-later
//
// Swift wrapper around the gomobile-bound Evmsdk.xcframework. The
// gomobile bind exposes every package-level function in
// cmd/evmsdk/mobile_facade.go as a static method on the `Evmsdk`
// namespace, e.g. Evmsdk.mobileInit(_:).
//
// This wrapper turns those C-style "" / non-empty error strings into
// Swift `throws` and decodes the JSON result blobs into typed structs
// for SwiftUI binding.

import Foundation
import Combine
import Evmsdk     // imported from the gomobile-bound .xcframework

class EvmsdkWrapper: ObservableObject {

    // MARK: - Published state for SwiftUI

    @Published var statusText = "Not initialized"
    @Published var isConnected = false
    @Published var blsKeyHex = ""
    @Published var blocksVerified: Int64 = 0
    @Published var successCount: Int64 = 0
    @Published var failureCount: Int64 = 0
    @Published var successRate: Int64 = 0
    @Published var avgTimeMs: Int64 = 0
    @Published var lastBlockInfo: BlockInfo?
    @Published var verifyLog: [LogEntry] = []
    @Published var producers: [Producer] = []
    @Published var activeProducerURI: String = ""
    @Published var rotationPolicy = RotationPolicy()

    // MARK: - Types

    struct BlockInfo: Identifiable {
        let id = UUID()
        let blockNumber: Int64
        let blockHash: String
        let computedReceiptsRoot: String
        let txCount: Int
        let witnessReadLogLen: Int
        let uncachedBytecodes: Int
        let packetSizeBytes: Int
        let verifyTimeMs: Int64
        let success: Bool
        let signature: String
    }

    struct LogEntry: Identifiable {
        let id = UUID()
        let blockNumber: Int64
        let success: Bool
        let verifyTimeMs: Int64
    }

    struct Producer: Identifiable {
        var id: String { uri }
        let uri: String
        let account: String
        let state: String
        let active: Bool
    }

    struct RotationPolicy {
        var blockTimeMs: Int64 = 1000
        var slowLeaderTimeoutMs: Int64 = 3000
        var rotationCycleMs: Int64 = 7000
        var monitorPeriodMs: Int64 = 500
    }

    enum WrapperError: Error, LocalizedError {
        case notInitialized
        case backend(String)
        case decode(String)

        var errorDescription: String? {
            switch self {
            case .notInitialized: return "Verifier not initialized"
            case .backend(let s): return "Backend error: \(s)"
            case .decode(let s): return "Decode error: \(s)"
            }
        }
    }

    // MARK: - Lifecycle

    func initialize(chainID: Int64 = 4242) {
        let err = EvmsdkMobileInit(chainID)
        if !err.isEmpty {
            DispatchQueue.main.async { self.statusText = "Init failed: \(err)" }
            return
        }
        DispatchQueue.main.async { self.statusText = "Initialized" }
    }

    func setPrivKey(_ hex: String) throws {
        let err = EvmsdkMobileSetPrivKey(hex)
        if !err.isEmpty { throw WrapperError.backend(err) }
        let pub = EvmsdkMobileGetPubkey()
        DispatchQueue.main.async { self.blsKeyHex = pub }
    }

    func setServer(host: String, port: UInt16, account: String) throws {
        let uri = "ws://\(host):\(port)"
        let err = EvmsdkMobileSetServer(uri, account)
        if !err.isEmpty { throw WrapperError.backend(err) }
        refreshProducers()
    }

    /// Adds an additional producer connection. Together with setServer this
    /// is how multi-node mining is configured: one producer per IDC node.
    func addProducer(uri: String, account: String) throws {
        let err = EvmsdkMobileAddProducer(uri, account)
        if !err.isEmpty { throw WrapperError.backend(err) }
        refreshProducers()
    }

    func removeProducer(uri: String) throws {
        let err = EvmsdkMobileRemoveProducer(uri)
        if !err.isEmpty { throw WrapperError.backend(err) }
        refreshProducers()
    }

    func refreshProducers() {
        let json = EvmsdkMobileListProducers()
        let activeURI = EvmsdkMobileGetActiveProducer()
        guard let data = json.data(using: .utf8),
              let list = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] else {
            return
        }
        let next = list.map { d in
            Producer(
                uri: d["uri"] as? String ?? "",
                account: d["account"] as? String ?? "",
                state: d["state"] as? String ?? "unknown",
                active: d["active"] as? Bool ?? false
            )
        }
        DispatchQueue.main.async {
            self.producers = next
            self.activeProducerURI = activeURI
        }
    }

    /// Pushes the local rotation policy values down to the SDK.
    func applyRotationPolicy() throws {
        let err = EvmsdkMobileSetRotationPolicy(
            rotationPolicy.blockTimeMs,
            rotationPolicy.slowLeaderTimeoutMs,
            rotationPolicy.rotationCycleMs,
            rotationPolicy.monitorPeriodMs
        )
        if !err.isEmpty { throw WrapperError.backend(err) }
    }

    func start() throws {
        let err = EvmsdkMobileStart()
        if !err.isEmpty { throw WrapperError.backend(err) }
        DispatchQueue.main.async {
            self.isConnected = true
            self.statusText = "Connected"
            self.startStatsPoller()
        }
    }

    func stop() {
        _ = EvmsdkMobileStop()
        DispatchQueue.main.async {
            self.isConnected = false
            self.statusText = "Disconnected"
        }
    }

    func free() {
        _ = EvmsdkMobileFree()
    }

    deinit {
        free()
    }

    // MARK: - Stats polling

    private var pollerTimer: Timer?

    private func startStatsPoller() {
        pollerTimer?.invalidate()
        pollerTimer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            self?.refreshStats()
            self?.refreshLastBlock()
            self?.refreshProducers()
        }
    }

    private func refreshStats() {
        let json = EvmsdkMobileGetStats()
        guard let data = json.data(using: .utf8),
              let dict = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return
        }
        DispatchQueue.main.async {
            self.blocksVerified = dict["blocks_verified"] as? Int64 ?? 0
            self.successCount   = dict["success_count"]   as? Int64 ?? 0
            self.failureCount   = dict["failure_count"]   as? Int64 ?? 0
            self.successRate    = dict["success_rate"]    as? Int64 ?? 0
            self.avgTimeMs      = dict["avg_time_ms"]     as? Int64 ?? 0
        }
    }

    private func refreshLastBlock() {
        let json = EvmsdkMobileGetLastVerifyInfo()
        guard json != "{}",
              let data = json.data(using: .utf8),
              let dict = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return
        }
        let info = BlockInfo(
            blockNumber: dict["block_number"] as? Int64 ?? 0,
            blockHash:   dict["block_hash"]   as? String ?? "",
            computedReceiptsRoot: dict["computed_receipts_root"] as? String ?? "",
            txCount:     dict["tx_count"]    as? Int ?? 0,
            witnessReadLogLen: dict["witness_read_log_len"] as? Int ?? 0,
            uncachedBytecodes: dict["uncached_bytecodes"]  as? Int ?? 0,
            packetSizeBytes:   dict["packet_size_bytes"]   as? Int ?? 0,
            verifyTimeMs:      dict["verify_time_ms"]      as? Int64 ?? 0,
            success:           dict["success"]              as? Bool ?? false,
            signature:         dict["signature"]            as? String ?? ""
        )
        DispatchQueue.main.async {
            // Only push to log on a new block number
            if self.lastBlockInfo?.blockNumber != info.blockNumber {
                self.verifyLog.insert(LogEntry(
                    blockNumber: info.blockNumber,
                    success: info.success,
                    verifyTimeMs: info.verifyTimeMs
                ), at: 0)
                if self.verifyLog.count > 100 {
                    self.verifyLog = Array(self.verifyLog.prefix(100))
                }
            }
            self.lastBlockInfo = info
        }
    }
}
