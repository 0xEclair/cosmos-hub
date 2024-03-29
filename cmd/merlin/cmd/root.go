package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/config"
	"github.com/cosmos/cosmos-sdk/client/debug"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/keys"
	"github.com/cosmos/cosmos-sdk/client/rpc"
	"github.com/cosmos/cosmos-sdk/server"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/snapshots"
	"github.com/cosmos/cosmos-sdk/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authcmd "github.com/cosmos/cosmos-sdk/x/auth/client/cli"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/crisis"
	genutilcli "github.com/cosmos/cosmos-sdk/x/genutil/client/cli"
	"github.com/spf13/cast"
	"github.com/spf13/cobra"
	tmcli "github.com/tendermint/tendermint/libs/cli"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"
	
	merlin "merlin/app"
	"merlin/app/params"
)

func NewRootCmd() (*cobra.Command, params.EncodingConfig) {
	encodingConfig := merlin.MakeEncodingConfig()
	initClientCtx := client.Context{}.
		WithCodec(encodingConfig.Marshaler).
		WithInterfaceRegistry(encodingConfig.InterfaceRegistry).
		WithTxConfig(encodingConfig.TxConfig).
		WithLegacyAmino(encodingConfig.Amino).
		WithInput(os.Stdin).
		WithAccountRetriever(types.AccountRetriever{}).
		WithHomeDir(merlin.DefaultNodeHome).
		// perhaps viper reads config.yml
		WithViper("")
	
	rootCmd := &cobra.Command{
		Use:   "merlind",
		Short: "Stargate Cosmos Hub App",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			initClientCtx, err := client.ReadPersistentCommandFlags(initClientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			initClientCtx, err = config.ReadFromClientConfig(initClientCtx)
			if err != nil {
				return err
			}
			
			if err = client.SetCmdClientContextHandler(initClientCtx, cmd); err != nil {
				return err
			}
			
			customTemplate, customMerlinConfig := initAppConfig()
			return server.InterceptConfigsPreRunHandler(cmd, customTemplate, customMerlinConfig)
		},
	}
	
	initRootCmd(rootCmd, encodingConfig)
	return rootCmd, encodingConfig
}

func initAppConfig() (string, interface{}) {
	type CustomAppConfig struct {
		serverconfig.Config
	}
	// allow overrides to the sdk default server config
	svrCfg := serverconfig.DefaultConfig()
	svrCfg.StateSync.SnapshotInterval = 1000
	svrCfg.StateSync.SnapshotKeepRecent = 10
	
	MerlinAppCfg := CustomAppConfig{ Config: *svrCfg }
	MerlinAppTemplate := serverconfig.DefaultConfigTemplate
	return MerlinAppTemplate, MerlinAppCfg
}

func initRootCmd(rootCmd *cobra.Command, encodingConfig params.EncodingConfig) {
	cfg := sdk.GetConfig()
	cfg.Seal()
	rootCmd.AddCommand(
		genutilcli.InitCmd(merlin.ModuleBasics, merlin.DefaultNodeHome),
		genutilcli.CollectGenTxsCmd(banktypes.GenesisBalancesIterator{}, merlin.DefaultNodeHome),
		genutilcli.GenTxCmd(merlin.ModuleBasics, encodingConfig.TxConfig, banktypes.GenesisBalancesIterator{}, merlin.DefaultNodeHome),
		genutilcli.ValidateGenesisCmd(merlin.ModuleBasics),
		AddGenesisAccountCmd(merlin.DefaultNodeHome),
		tmcli.NewCompletionCmd(rootCmd, true),
		testnetCmd(merlin.ModuleBasics, banktypes.GenesisBalancesIterator{}),
		debug.Cmd(),
		config.Cmd(),
	)
	
	ac := appCreator { encCfg: encodingConfig }
	
	server.AddCommands(rootCmd, merlin.DefaultNodeHome, ac.newApp, ac.appExport, addModuleInitFlags)
	
	rootCmd.AddCommand(
		rpc.StatusCommand(),
		queryCommand(),
		txCommand(),
		keys.Commands(merlin.DefaultNodeHome),
	)
}

type appCreator struct {
	encCfg params.EncodingConfig
}

func (ac appCreator) newApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	appOpts servertypes.AppOptions,
) servertypes.Application {
	var cache sdk.MultiStorePersistentCache
	
	if cast.ToBool(appOpts.Get(server.FlagInterBlockCache)) {
		cache = store.NewCommitKVStoreCacheManager()
	}
	
	skipUpgradeHeights := make(map[int64]bool)
	for _, h := range cast.ToIntSlice(appOpts.Get(server.FlagUnsafeSkipUpgrades)) {
		skipUpgradeHeights[int64(h)] = true
	}
	
	pruningOpts, err := server.GetPruningOptionsFromFlags(appOpts)
	if err != nil {
		panic(err)
	}
	snapshotDir := filepath.Join(cast.ToString(appOpts.Get(flags.FlagHome)), "data", "snapshots")
	snapshotDB, err := sdk.NewLevelDB("metadata", snapshotDir)
	if err!= nil {
		panic(err)
	}
	snapshotStore, err := snapshots.NewStore(snapshotDB, snapshotDir)
	if err!= nil {
		panic(err)
	}
	
	return merlin.NewMerlinApp(
		logger, db, traceStore, true, skipUpgradeHeights,
		cast.ToString(appOpts.Get(flags.FlagHome)),
		cast.ToUint(appOpts.Get(server.FlagInvCheckPeriod)),
		ac.encCfg,
		appOpts,
		baseapp.SetPruning(pruningOpts),
		baseapp.SetMinGasPrices(cast.ToString(appOpts.Get(server.FlagMinGasPrices))),
		baseapp.SetHaltHeight(cast.ToUint64(appOpts.Get(server.FlagHaltHeight))),
		baseapp.SetHaltTime(cast.ToUint64(appOpts.Get(server.FlagHaltTime))),
		baseapp.SetMinRetainBlocks(cast.ToUint64(appOpts.Get(server.FlagMinRetainBlocks))),
		baseapp.SetInterBlockCache(cache),
		baseapp.SetTrace(cast.ToBool(appOpts.Get(server.FlagTrace))),
		baseapp.SetIndexEvents(cast.ToStringSlice(appOpts.Get(server.FlagIndexEvents))),
		baseapp.SetSnapshotStore(snapshotStore),
		baseapp.SetSnapshotInterval(cast.ToUint64(appOpts.Get(server.FlagStateSyncSnapshotInterval))),
		baseapp.SetSnapshotKeepRecent(cast.ToUint32(appOpts.Get(server.FlagStateSyncSnapshotKeepRecent))),
	)
}

func (ac appCreator) appExport(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	height int64,
	forZeroHeight bool,
	jailAllowedAddrs []string,
	appOpts servertypes.AppOptions,
) (servertypes.ExportedApp, error) {
	homePath, ok := appOpts.Get(flags.FlagHome).(string)
	if !ok || homePath == "" {
		return servertypes.ExportedApp{}, errors.New("application home is not set")
	}
	
	var loadLatest bool
	if height == -1 {
		loadLatest = true
	}
	merlinApp := merlin.NewMerlinApp(
		logger, db,
		traceStore, loadLatest,
		map[int64]bool{},
		homePath,
		cast.ToUint(appOpts.Get(server.FlagInvCheckPeriod)),
		ac.encCfg,
		appOpts,
	)
	
	if height != -1 {
		if err:= merlinApp.LoadHeight(height); err != nil {
			return servertypes.ExportedApp{}, err
		}
	}
	
	return merlinApp.ExportAppStateAndValidators(forZeroHeight, jailAllowedAddrs)
}

func addModuleInitFlags(startCmd *cobra.Command) {
	crisis.AddModuleInitFlags(startCmd)
}

func queryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "query",
		Aliases: []string{"q"},
		Short: "Querying subcommands",
		DisableFlagParsing: true,
		SuggestionsMinimumDistance: 2,
		RunE: client.ValidateCmd,
	}
	
	cmd.AddCommand(
		authcmd.GetAccountCmd(),
		rpc.ValidatorCommand(),
		rpc.BlockCommand(),
		authcmd.QueryTxsByEventsCmd(),
		authcmd.QueryTxCmd(),
	)
	merlin.ModuleBasics.AddQueryCommands(cmd)
	cmd.PersistentFlags().String(flags.FlagChainID, "", "The network chain ID")
	
	return cmd
}

func txCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "tx",
		Short:                      "Transactions subcommands",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	
	cmd.AddCommand(
		authcmd.GetSignCommand(),
		authcmd.GetSignBatchCommand(),
		authcmd.GetMultiSignCommand(),
		authcmd.GetMultiSignBatchCmd(),
		authcmd.GetValidateSignaturesCommand(),
		flags.LineBreak,
		authcmd.GetBroadcastCommand(),
		authcmd.GetEncodeCommand(),
		authcmd.GetDecodeCommand(),
	)
	
	merlin.ModuleBasics.AddTxCommands(cmd)
	cmd.PersistentFlags().String(flags.FlagChainID, "", "The network chain ID")
	
	return cmd
}