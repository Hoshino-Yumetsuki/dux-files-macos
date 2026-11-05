cmake_minimum_required(VERSION 3.22)
set(CMAKE_OSX_DEPLOYMENT_TARGET "13.0" CACHE STRING "Minimum macOS version")
project(DuxFiles VERSION 0.1.0 LANGUAGES CXX OBJCXX)

if(NOT APPLE)
    message(FATAL_ERROR "This launcher targets macOS; use the existing Quickshell build on Linux")
endif()

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_OBJCXX_STANDARD 17)
set(CMAKE_OBJCXX_STANDARD_REQUIRED ON)
set_source_files_properties(runtime/cocoa.mm native/main.mm native/apps.mm native/clipboard.mm
    native/volumes.mm native/trash.mm native/search.mm native/places.mm native/metadata.mm native/cloud.mm
    native/fileops.mm native/fileops_coordination.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc")
option(DFILES_REQUIRE_STATIC_QT "Reject shared Qt in distributable builds" OFF)
find_package(Qt6 6.8 REQUIRED COMPONENTS Core Gui Qml Quick QuickControls2 Network Widgets Svg)
find_package(Python3 REQUIRED COMPONENTS Interpreter)
get_target_property(qt_core_type Qt6::Core TYPE)
if(DFILES_REQUIRE_STATIC_QT AND NOT qt_core_type STREQUAL "STATIC_LIBRARY")
    message(FATAL_ERROR "Release packaging requires a static Qt build; see macos/README.md")
endif()

set(QML_ROOT "${CMAKE_CURRENT_BINARY_DIR}/qml")
set(QML_SOURCE_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../quickshell")
file(GLOB_RECURSE QML_INPUTS FOLLOW_SYMLINKS LIST_DIRECTORIES false CONFIGURE_DEPENDS "${QML_SOURCE_ROOT}/*")
file(GLOB QML_OVERRIDES CONFIGURE_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/runtime/qml/*")
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS
    ${QML_INPUTS} ${QML_OVERRIDES} "${CMAKE_CURRENT_SOURCE_DIR}/stage-qml.py")
execute_process(
    COMMAND "${Python3_EXECUTABLE}" -I "${CMAKE_CURRENT_SOURCE_DIR}/stage-qml.py"
            "${CMAKE_CURRENT_SOURCE_DIR}/../quickshell" "${QML_ROOT}/qs"
    COMMAND_ERROR_IS_FATAL ANY
)
set(DFILES_BACKEND "${CMAKE_CURRENT_SOURCE_DIR}/../core/bin/dfiles" CACHE FILEPATH "CGO-free Go backend")
qt_add_executable(dfiles-ui MACOSX_BUNDLE
    main.cpp runtime/io.cpp runtime/io.h runtime/shell.cpp runtime/shell.h runtime/cocoa.mm native/clipboard.mm
    ../assets/dev.duxlabs.files.icns
)
set_source_files_properties(../assets/dev.duxlabs.files.icns PROPERTIES MACOSX_PACKAGE_LOCATION Resources)
set(DFILES_NATIVE_FRAMEWORKS
    "-framework AppKit" "-framework CoreServices" "-framework UniformTypeIdentifiers"
    "-framework CoreGraphics" "-framework ImageIO" "-framework AVFoundation" "-framework CoreMedia" "-framework QuickLookThumbnailing" "-framework Quartz")
target_link_libraries(dfiles-ui PRIVATE
    Qt6::Core Qt6::Gui Qt6::Qml Qt6::Quick Qt6::QuickControls2 Qt6::Network Qt6::Widgets Qt6::Svg
    ${DFILES_NATIVE_FRAMEWORKS} "-framework QuartzCore"
)
qt_add_executable(dfiles-native
    native/main.mm native/apps.mm native/clipboard.mm native/volumes.mm native/trash.mm native/search.mm native/places.mm
    native/metadata.mm native/cloud.mm native/fileops.mm native/fileops_coordination.mm runtime/cocoa.mm)
target_link_libraries(dfiles-native PRIVATE Qt6::Core Qt6::Gui Qt6::Qml ${DFILES_NATIVE_FRAMEWORKS}
    "-framework QuartzCore" "-framework DiskArbitration" "-framework NetFS" "-framework IOKit")
target_compile_definitions(dfiles-ui PRIVATE DFILES_QML_ROOT="${QML_ROOT}")
set_target_properties(dfiles-ui PROPERTIES
    OUTPUT_NAME "Dux Files"
    MACOSX_BUNDLE_GUI_IDENTIFIER dev.duxlabs.files
    MACOSX_BUNDLE_BUNDLE_NAME "Dux Files"
    MACOSX_BUNDLE_ICON_FILE dev.duxlabs.files.icns
    MACOSX_BUNDLE_INFO_PLIST "${CMAKE_CURRENT_SOURCE_DIR}/Info.plist.in"
)
file(GLOB_RECURSE qml_sources CONFIGURE_DEPENDS "${QML_ROOT}/qs/*.qml" "${QML_ROOT}/qs/*.js")
qt_add_resources(dfiles-ui dux_qml_scan PREFIX "/dux" BASE "${QML_ROOT}" FILES ${qml_sources})
qt_add_resources(dfiles-ui dux_runtime_qml PREFIX "/dux/runtime" BASE "${CMAKE_CURRENT_SOURCE_DIR}/runtime/qml"
    FILES runtime/qml/ClippingRectangle.qml runtime/qml/IconImage.qml runtime/qml/LazyLoader.qml
)
set_property(TARGET dfiles-ui PROPERTY QT_QML_IMPORT_PATH "${QML_ROOT}")
set_property(TARGET dfiles-ui PROPERTY QT_QML_IMPORT_SCANNER_EXTRA_ROOT_PATHS "${QML_ROOT}")
if(qt_core_type STREQUAL "STATIC_LIBRARY")
    qt_import_plugins(dfiles-ui INCLUDE Qt6::QCocoaIntegrationPlugin Qt6::QSvgPlugin)
endif()
qt_import_qml_plugins(dfiles-ui)
add_custom_target(sync-bundle ALL
    COMMAND "${CMAKE_COMMAND}" -E make_directory "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/MacOS"
    COMMAND "${CMAKE_COMMAND}" -E make_directory "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/Resources"
    COMMAND "${CMAKE_COMMAND}" -E remove_directory "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/Resources/qml"
    COMMAND "${CMAKE_COMMAND}" -E copy_directory "${QML_ROOT}" "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/Resources/qml"
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${DFILES_BACKEND}" "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/MacOS/dfiles"
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different "$<TARGET_FILE:dfiles-native>" "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/MacOS/dfiles-native"
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${CMAKE_CURRENT_SOURCE_DIR}/../LICENSE" "$<TARGET_BUNDLE_DIR:dfiles-ui>/Contents/Resources/LICENSE"
    DEPENDS dfiles-ui dfiles-native
)
install(TARGETS dfiles-ui BUNDLE DESTINATION .)

option(DFILES_BUILD_TESTS "Build the macOS compatibility checks" OFF)
if(DFILES_BUILD_TESTS)
    enable_testing()
    qt_add_executable(io_test tests/io_test.cpp runtime/io.cpp runtime/io.h)
    target_include_directories(io_test PRIVATE runtime)
    target_link_libraries(io_test PRIVATE Qt6::Core Qt6::Qml Qt6::Network)
    qt_add_executable(shell_test tests/shell_test.cpp runtime/shell.cpp runtime/shell.h runtime/io.cpp runtime/io.h runtime/cocoa.mm native/clipboard.mm)
    target_link_libraries(shell_test PRIVATE
        Qt6::Core Qt6::Gui Qt6::Qml Qt6::Quick Qt6::Widgets Qt6::Svg Qt6::Network
        ${DFILES_NATIVE_FRAMEWORKS} "-framework QuartzCore")
    qt_add_executable(volumes_test ../core/internal/devices/testdata/volumes_test.mm)
    set_source_files_properties(../core/internal/devices/testdata/volumes_test.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc;-UNDEBUG")
    target_link_libraries(volumes_test PRIVATE Qt6::Core ${DFILES_NATIVE_FRAMEWORKS}
        "-framework DiskArbitration" "-framework NetFS" "-framework IOKit")
    qt_add_executable(clipboard_test tests/clipboard_test.mm)
    set_source_files_properties(tests/clipboard_test.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc")
    target_link_libraries(clipboard_test PRIVATE Qt6::Core Qt6::Gui "-framework AppKit" "-framework ImageIO")
    qt_add_executable(trash_test tests/trash_test.mm)
    set_source_files_properties(tests/trash_test.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc")
    target_link_libraries(trash_test PRIVATE Qt6::Core "-framework Foundation")
    qt_add_executable(places_test tests/places_test.mm)
    set_source_files_properties(tests/places_test.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc;-UNDEBUG")
    target_link_libraries(places_test PRIVATE Qt6::Core ${DFILES_NATIVE_FRAMEWORKS})
    qt_add_executable(metadata_test tests/metadata_test.mm)
    set_source_files_properties(tests/metadata_test.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc;-UNDEBUG")
    target_link_libraries(metadata_test PRIVATE Qt6::Core ${DFILES_NATIVE_FRAMEWORKS})
    qt_add_executable(cloud_test tests/cloud_test.mm)
    set_source_files_properties(tests/cloud_test.mm PROPERTIES COMPILE_OPTIONS "-fobjc-arc;-UNDEBUG")
    target_link_libraries(cloud_test PRIVATE Qt6::Core ${DFILES_NATIVE_FRAMEWORKS})
    if(qt_core_type STREQUAL "STATIC_LIBRARY")
        find_package(Qt6 REQUIRED COMPONENTS QuickTest)
        target_link_libraries(dfiles-ui PRIVATE Qt6::QuickTestplugin)
        qt_import_plugins(dfiles-ui INCLUDE Qt6::QOffscreenIntegrationPlugin)
        target_link_libraries(io_test PRIVATE Qt6::qmlplugin Qt6::modelsplugin Qt6::workerscriptplugin)
        set_property(TARGET shell_test PROPERTY QT_QML_IMPORT_PATH "${QML_ROOT}")
        set_property(TARGET shell_test PROPERTY QT_QML_IMPORT_SCANNER_EXTRA_ROOT_PATHS "${QML_ROOT}")
        qt_import_qml_plugins(shell_test)
        qt_import_plugins(shell_test INCLUDE Qt6::QCocoaIntegrationPlugin Qt6::QOffscreenIntegrationPlugin Qt6::QSvgPlugin)
        qt_import_plugins(clipboard_test INCLUDE Qt6::QOffscreenIntegrationPlugin)
    endif()
    add_test(NAME io COMMAND io_test)
    add_test(NAME native-clipboard COMMAND clipboard_test)
    add_test(NAME native-volumes COMMAND volumes_test)
    add_test(NAME native-trash COMMAND trash_test)
    add_test(NAME native-places COMMAND places_test)
    add_test(NAME native-metadata COMMAND metadata_test)
    add_test(NAME native-cloud COMMAND cloud_test)
    add_test(NAME native-helper COMMAND "${Python3_EXECUTABLE}" -I "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_native.py" $<TARGET_FILE:dfiles-native>)
    add_test(NAME file-clipboard COMMAND shell_test "${QML_SOURCE_ROOT}" --clipboard-test)
    add_test(NAME native-media COMMAND shell_test "${QML_ROOT}/qs" --media-test)
    add_test(NAME host COMMAND "${Python3_EXECUTABLE}" -I "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_host.py" $<TARGET_FILE:dfiles-ui>)
    add_test(NAME qml-interactions COMMAND "${Python3_EXECUTABLE}" -I "${QML_SOURCE_ROOT}/tests/run-qml.py" --macos-host $<TARGET_FILE:dfiles-ui> shell)
    add_test(NAME packaging COMMAND "${Python3_EXECUTABLE}" -I -m unittest discover -s "${CMAKE_CURRENT_SOURCE_DIR}/tests" -v)
    set_tests_properties(host qml-interactions PROPERTIES TIMEOUT 90)
endif()
