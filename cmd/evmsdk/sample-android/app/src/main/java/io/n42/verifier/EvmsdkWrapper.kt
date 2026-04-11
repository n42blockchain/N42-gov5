// SPDX-License-Identifier: LGPL-3.0-or-later
//
// Kotlin wrapper around the gomobile-bound evmsdk.aar. The gomobile
// bind exposes every package-level function in cmd/evmsdk/mobile_facade.go
// as a static method on the `Evmsdk` class, e.g. Evmsdk.mobileInit(4242L).
//
// This wrapper turns "" / non-empty error strings into Kotlin exceptions
// and decodes the JSON result blobs into typed data classes for Compose.

package io.n42.verifier

import evmsdk.Evmsdk   // imported from the gomobile-bound .aar
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONObject

class EvmsdkWrapper {

    // ─── Published state for Compose ───────────────────────────────────

    private val _status = MutableStateFlow("Not initialized")
    val status: StateFlow<String> = _status

    private val _isConnected = MutableStateFlow(false)
    val isConnected: StateFlow<Boolean> = _isConnected

    private val _blsKeyHex = MutableStateFlow("")
    val blsKeyHex: StateFlow<String> = _blsKeyHex

    private val _stats = MutableStateFlow(Stats())
    val stats: StateFlow<Stats> = _stats

    private val _lastBlock = MutableStateFlow<BlockInfo?>(null)
    val lastBlock: StateFlow<BlockInfo?> = _lastBlock

    private val _verifyLog = MutableStateFlow<List<LogEntry>>(emptyList())
    val verifyLog: StateFlow<List<LogEntry>> = _verifyLog

    private val _producers = MutableStateFlow<List<Producer>>(emptyList())
    val producers: StateFlow<List<Producer>> = _producers

    private val _activeProducerURI = MutableStateFlow("")
    val activeProducerURI: StateFlow<String> = _activeProducerURI

    private val _policy = MutableStateFlow(RotationPolicy())
    val policy: StateFlow<RotationPolicy> = _policy

    // ─── Types ─────────────────────────────────────────────────────────

    data class Stats(
        val blocksVerified: Long = 0,
        val successCount: Long = 0,
        val failureCount: Long = 0,
        val successRate: Long = 0,
        val avgTimeMs: Long = 0,
    )

    data class BlockInfo(
        val blockNumber: Long,
        val blockHash: String,
        val computedReceiptsRoot: String,
        val txCount: Int,
        val witnessReadLogLen: Int,
        val uncachedBytecodes: Int,
        val packetSizeBytes: Int,
        val verifyTimeMs: Long,
        val success: Boolean,
        val signature: String,
    )

    data class LogEntry(
        val blockNumber: Long,
        val success: Boolean,
        val verifyTimeMs: Long,
    )

    data class Producer(
        val uri: String,
        val account: String,
        val state: String,
        val active: Boolean = false,
    )

    data class RotationPolicy(
        val blockTimeMs: Long = 1000,
        val slowLeaderTimeoutMs: Long = 3000,
        val rotationCycleMs: Long = 7000,
        val monitorPeriodMs: Long = 500,
    )

    class WrapperException(message: String) : Exception(message)

    // ─── Lifecycle ─────────────────────────────────────────────────────

    fun initialize(chainID: Long = 4242L) {
        val err = Evmsdk.mobileInit(chainID)
        _status.value = if (err.isEmpty()) "Initialized" else "Init failed: $err"
    }

    fun setPrivKey(hex: String) {
        val err = Evmsdk.mobileSetPrivKey(hex)
        if (err.isNotEmpty()) throw WrapperException(err)
        _blsKeyHex.value = Evmsdk.mobileGetPubkey()
    }

    fun setServer(host: String, port: Int, account: String) {
        val uri = "ws://$host:$port"
        val err = Evmsdk.mobileSetServer(uri, account)
        if (err.isNotEmpty()) throw WrapperException(err)
        refreshProducers()
    }

    /** Adds an additional producer connection for multi-node mining. */
    fun addProducer(uri: String, account: String) {
        val err = Evmsdk.mobileAddProducer(uri, account)
        if (err.isNotEmpty()) throw WrapperException(err)
        refreshProducers()
    }

    fun removeProducer(uri: String) {
        val err = Evmsdk.mobileRemoveProducer(uri)
        if (err.isNotEmpty()) throw WrapperException(err)
        refreshProducers()
    }

    fun refreshProducers() {
        val json = Evmsdk.mobileListProducers()
        val arr = try { org.json.JSONArray(json) } catch (_: Exception) { return }
        val list = mutableListOf<Producer>()
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            list += Producer(
                uri     = o.optString("uri"),
                account = o.optString("account"),
                state   = o.optString("state"),
                active  = o.optBoolean("active"),
            )
        }
        _producers.value = list
        _activeProducerURI.value = Evmsdk.mobileGetActiveProducer()
    }

    fun applyRotationPolicy(p: RotationPolicy) {
        val err = Evmsdk.mobileSetRotationPolicy(
            p.blockTimeMs,
            p.slowLeaderTimeoutMs,
            p.rotationCycleMs,
            p.monitorPeriodMs,
        )
        if (err.isNotEmpty()) throw WrapperException(err)
        _policy.value = p
    }

    fun start() {
        val err = Evmsdk.mobileStart()
        if (err.isNotEmpty()) throw WrapperException(err)
        _isConnected.value = true
        _status.value = "Connected"
        startStatsPoller()
    }

    fun stop() {
        Evmsdk.mobileStop()
        stopStatsPoller()
        _isConnected.value = false
        _status.value = "Disconnected"
    }

    fun free() {
        Evmsdk.mobileFree()
    }

    // ─── Stats polling ─────────────────────────────────────────────────

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var pollerJob: Job? = null

    private fun startStatsPoller() {
        pollerJob?.cancel()
        pollerJob = scope.launch {
            while (isActive) {
                refreshStats()
                refreshLastBlock()
                refreshProducers()
                delay(1000)
            }
        }
    }

    private fun stopStatsPoller() {
        pollerJob?.cancel()
        pollerJob = null
    }

    private fun refreshStats() {
        val json = Evmsdk.mobileGetStats()
        val obj = try { JSONObject(json) } catch (_: Exception) { return }
        _stats.value = Stats(
            blocksVerified = obj.optLong("blocks_verified"),
            successCount   = obj.optLong("success_count"),
            failureCount   = obj.optLong("failure_count"),
            successRate    = obj.optLong("success_rate"),
            avgTimeMs      = obj.optLong("avg_time_ms"),
        )
    }

    private fun refreshLastBlock() {
        val json = Evmsdk.mobileGetLastVerifyInfo()
        if (json == "{}") return
        val obj = try { JSONObject(json) } catch (_: Exception) { return }
        val info = BlockInfo(
            blockNumber          = obj.optLong("block_number"),
            blockHash            = obj.optString("block_hash"),
            computedReceiptsRoot = obj.optString("computed_receipts_root"),
            txCount              = obj.optInt("tx_count"),
            witnessReadLogLen    = obj.optInt("witness_read_log_len"),
            uncachedBytecodes    = obj.optInt("uncached_bytecodes"),
            packetSizeBytes      = obj.optInt("packet_size_bytes"),
            verifyTimeMs         = obj.optLong("verify_time_ms"),
            success              = obj.optBoolean("success"),
            signature            = obj.optString("signature"),
        )
        if (_lastBlock.value?.blockNumber != info.blockNumber) {
            _verifyLog.value = (listOf(
                LogEntry(info.blockNumber, info.success, info.verifyTimeMs)
            ) + _verifyLog.value).take(100)
        }
        _lastBlock.value = info
    }
}
